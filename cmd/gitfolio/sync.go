package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// syncState remembers what aline.team already has, so only new or changed records are sent (DESIGN 6.2).
type syncState struct {
	Account   string            `json:"account,omitempty"`   // the aline.team account the records below were sent to
	Server    string            `json:"server,omitempty"`    // and the server it is on (Credentials.Server)
	Commits   map[string]string `json:"commits,omitempty"`   // provider/namespace/hash → fingerprint of the record sent
	Deletes   []deletion        `json:"deletes,omitempty"`   // repository deletions not yet accepted by the server
	Moves     []repoMove        `json:"moves,omitempty"`     // git service address changes not yet sent (remote set-url)
	Changed   map[string]string `json:"changed,omitempty"`   // provider/namespace/path → modifiedAt sent (modifiedFiles)
	Deps      map[string]string `json:"deps,omitempty"`      // provider/namespace → fingerprint of the dependencies sent
	Stats     map[string]string `json:"stats,omitempty"`     // provider/namespace → fingerprint of the snapshot sent (statsPrint)
	NoStats   time.Time         `json:"noStats,omitzero"`    // when aline.team last said it takes no snapshots yet
	LastSync  string            `json:"lastSync,omitempty"`  // last attempt, shown by `gitfolio schedule`
	LastError string            `json:"lastError,omitempty"` // why it failed; empty when it went through
	// Waiting maps provider/namespace to the email aline.team wants verified before it takes that
	// repository's commits (A013); they are sent again by every sync until then. Rebuilt by each sync.
	Waiting map[string]string `json:"waiting,omitempty"`
}

// repoMove tells aline.team that a registered repository's git service address changed (`git remote
// set-url`, or its push URL now counts), so it renames the repository it has instead of getting the
// commits again under the new name.
type repoMove struct {
	From deletion `json:"from"`
	To   deletion `json:"to"`
}

// deletion asks aline.team to delete one repository's commits (remove --purge).
type deletion struct {
	Provider  string `json:"provider"`
	Namespace string `json:"namespace"`
}

// syncPayload is what the next sync sends, in order.
type syncPayload struct {
	Deletes  []deletion
	Commits  []Commit
	Stats    []snapshot
	modified map[string][]modifiedFile // provider/namespace → first changes by others not sent yet
	deps     map[string][]depName      // provider/namespace → dependencies of the modules the user's commits touched
	noRemote int                       // commits of repositories without a git service remote, never sent
}

// modifiedFile tells aline.team when someone else first changed a file the user created.
type modifiedFile struct {
	Name       string `json:"name"`
	ModifiedAt string `json:"modifiedAt"`
}

// depsRequest is one PUT /cli/dependencies request: one repository's dependencies, which aline.team turns
// into its tech stack. Names only: versions and the files they were found in stay here.
type depsRequest struct {
	Provider     string    `json:"provider"`
	Namespace    string    `json:"namespace"`
	Dependencies []depName `json:"dependencies"`
}

type depName struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// commitBatch is one POST /cli/commits/batch request: one repository and its commits.
// The server knows the repository by provider and namespace; the local name is not sent.
type commitBatch struct {
	Provider      string         `json:"provider"`
	Namespace     string         `json:"namespace"`
	AuthorEmail   string         `json:"authorEmail"` // the primary work email, for all of the user's commits
	Commits       []Commit       `json:"commits"`
	ModifiedFiles []modifiedFile `json:"modifiedFiles,omitempty"` // first changes by others, at most maxModified
}

// request builds the body for commits of one repository: the repository fields and the author email
// move to the top, and every commit goes under author, the primary work email.
func request(commits []Commit, author string, modified []modifiedFile) commitBatch {
	b := commitBatch{Provider: commits[0].Provider, Namespace: commits[0].Namespace, AuthorEmail: cmp.Or(author, commits[0].AuthorEmail), ModifiedFiles: modified}
	for _, c := range commits {
		c.Provider, c.Namespace, c.Repo, c.AuthorEmail = "", "", "", ""
		b.Commits = append(b.Commits, c)
	}
	return b
}

// How much one request carries; longer values are cut or left out.
const (
	batchSize     = 500   // records per request
	maxMessage    = 10000 // characters
	maxFilesSent  = 1000
	maxModified   = 5000 // modifiedFiles per request
	maxNamespace  = 200  // longest owner/repo sent
	maxDeps       = 5000 // dependencies per repository
	maxDepName    = 214  // longest dependency name
	syncStateFile = "sync.json"
)

func loadSync(dir string) (st syncState, err error) {
	err = loadJSON(filepath.Join(dir, syncStateFile), &st)
	if st.Commits == nil {
		st.Commits = map[string]string{}
	}
	if st.Changed == nil {
		st.Changed = map[string]string{}
	}
	if st.Deps == nil {
		st.Deps = map[string]string{}
	}
	if st.Stats == nil {
		st.Stats = map[string]string{}
	}
	return st, err
}

func saveSync(dir string, st syncState) error { return saveJSON(filepath.Join(dir, syncStateFile), st) }

func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func commitKey(c Commit) string { return c.Provider + "/" + c.Namespace + "/" + c.Hash }

// statsPrint is the fingerprint of a snapshot without its asOf, so newer commits by others that change no
// count do not send it again.
func statsPrint(s snapshot) string {
	s.AsOf = ""
	return fingerprint(s)
}

// queueDeletion records a repository deletion for the next sync and forgets what was sent for it, so
// the commits are sent again if the repository is added back. Callers hold the data lock.
func queueDeletion(dir string, d deletion) error {
	if d.Namespace == "" {
		return nil // never sent: nothing to delete on the server
	}
	st, err := loadSync(dir)
	if err != nil {
		return err
	}
	for _, m := range []map[string]string{st.Commits, st.Changed} {
		for k := range m {
			if strings.HasPrefix(k, d.Provider+"/"+d.Namespace+"/") {
				delete(m, k)
			}
		}
	}
	delete(st.Stats, d.Provider+"/"+d.Namespace)
	if !slices.Contains(st.Deletes, d) {
		st.Deletes = append(st.Deletes, d)
	}
	return saveSync(dir, st)
}

// queueMove records a repository's new git service address for the next sync. Moves chain (A→B then
// B→C is A→C) and one back to where it started cancels. Callers hold the data lock.
func queueMove(dir string, mv repoMove) error {
	st, err := loadSync(dir)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(st.Moves, func(x repoMove) bool { return x.To == mv.From }); i >= 0 {
		mv.From = st.Moves[i].From
		st.Moves = slices.Delete(st.Moves, i, i+1)
	}
	if mv.From != mv.To {
		st.Moves = append(st.Moves, mv)
	}
	return saveSync(dir, st)
}

// renameSent moves what was sent for a repository to its new name, so commits aline.team already has
// are not sent again. A record counts as sent when its fingerprint under the old name matches.
func renameSent(st *syncState, mv repoMove, commits []Commit) {
	for _, c := range commits {
		if c.Provider != mv.To.Provider || c.Namespace != mv.To.Namespace {
			continue
		}
		c = sendable(c)
		old := c
		old.Provider, old.Namespace = mv.From.Provider, mv.From.Namespace
		if st.Commits[commitKey(old)] == fingerprint(old) {
			st.Commits[commitKey(c)] = fingerprint(c)
		}
		delete(st.Commits, commitKey(old))
	}
	delete(st.Stats, mv.From.Provider+"/"+mv.From.Namespace) // the snapshot goes again under the new name
	oldKey, newKey := mv.From.Provider+"/"+mv.From.Namespace+"/", mv.To.Provider+"/"+mv.To.Namespace+"/"
	for k, at := range st.Changed {
		if name, ok := strings.CutPrefix(k, oldKey); ok {
			st.Changed[newKey+name] = at
			delete(st.Changed, k)
		}
	}
}

// sendable cuts a record to the server's limits, as it is sent and fingerprinted.
func sendable(c Commit) Commit {
	if utf8.RuneCountInString(c.Message) > maxMessage {
		c.Message = string([]rune(c.Message)[:maxMessage])
	}
	if len(c.Files) > maxFilesSent {
		c.Files = c.Files[:maxFilesSent]
	}
	return c
}

// pending works out what aline.team does not have yet. Records are the ones `gitfolio export` shows,
// cut to the server's limits.
func pending(dir string, st syncState) (syncPayload, error) {
	p := syncPayload{Deletes: append([]deletion{}, st.Deletes...), modified: map[string][]modifiedFile{}, deps: map[string][]depName{}}
	out, err := buildExport(dir)
	if err != nil {
		return p, err
	}
	for _, c := range out.Commits {
		if c.Namespace == "" || len(c.Namespace) > maxNamespace {
			p.noRemote++
			continue
		}
		if c.Branch == "" { // not on a remote-tracking branch: read from HEAD before the first push succeeded
			continue
		}
		c = sendable(c)
		if st.Commits[commitKey(c)] != fingerprint(c) {
			p.Commits = append(p.Commits, c)
		}
	}
	for _, s := range out.Snapshots {
		if len(s.Days) > 0 && len(s.Namespace) <= maxNamespace && st.Stats[s.Provider+"/"+s.Namespace] != statsPrint(s) {
			p.Stats = append(p.Stats, s)
		}
	}
	for _, d := range out.Dependencies {
		if d.Namespace != "" && len(d.Namespace) <= maxNamespace && len(d.Name) <= maxDepName {
			key := d.Provider + "/" + d.Namespace
			p.deps[key] = append(p.deps[key], depName{d.Ecosystem, d.Name})
		}
	}
	for key, list := range p.deps {
		slices.SortFunc(list, func(a, b depName) int {
			return cmp.Or(strings.Compare(a.Ecosystem, b.Ecosystem), strings.Compare(a.Name, b.Name))
		})
		list = slices.Compact(list)
		p.deps[key] = list[:min(len(list), maxDeps)]
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return p, err
	}
	for _, r := range repos {
		key := r.Provider + "/" + r.Namespace
		for _, name := range slices.Sorted(maps.Keys(r.Changed)) {
			if at := r.Changed[name]; r.Namespace != "" && st.Changed[key+"/"+name] != at {
				p.modified[key] = append(p.modified[key], modifiedFile{name, at})
			}
		}
	}
	return p, nil
}

// takeModified hands out the first changes of repository key for one request, at most maxModified.
// aline.team takes them only in a request with commits, so a repository without new commits keeps them
// for a later sync.
func (p *syncPayload) takeModified(key string) []modifiedFile {
	m := p.modified[key]
	n := min(len(m), maxModified)
	p.modified[key] = m[n:]
	return m[:n]
}

// depsChanged lists the dependency requests aline.team does not have yet: for repositories it has commits
// of (it ignores dependencies of a repository it does not know), and an empty list where all are gone
// (deps off, or no module touched any more). Repositories waiting for a verified email wait for these too.
func (p syncPayload) depsChanged(st syncState, known map[string]bool) []depsRequest {
	keys := slices.Collect(maps.Keys(p.deps))
	for key := range st.Deps {
		if _, ok := p.deps[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var out []depsRequest
	for _, key := range keys {
		list := p.deps[key]
		_, waiting := st.Waiting[key]
		if waiting || (!known[key] && st.Deps[key] == "") || st.Deps[key] == depsPrint(list) {
			continue
		}
		provider, namespace, _ := strings.Cut(key, "/")
		out = append(out, depsRequest{provider, namespace, append([]depName{}, list...)})
	}
	return out
}

// depsPrint is the fingerprint of a dependency list as sent; "" for none.
func depsPrint(list []depName) string {
	if len(list) == 0 {
		return ""
	}
	return fingerprint(list)
}

// sentRepos is the repositories aline.team has commits or a snapshot of, as provider/namespace.
func sentRepos(st syncState, more []Commit) map[string]bool {
	known := map[string]bool{}
	for key := range st.Commits {
		known[key[:strings.LastIndex(key, "/")]] = true
	}
	for key := range st.Stats {
		known[key] = true
	}
	for _, c := range more {
		known[c.Provider+"/"+c.Namespace] = true
	}
	return known
}

// batches splits commits into requests of one repository each, at most batchSize records.
func batches(commits []Commit) [][]Commit {
	commits = slices.Clone(commits)
	slices.SortStableFunc(commits, func(a, b Commit) int {
		return strings.Compare(a.Provider+"/"+a.Namespace, b.Provider+"/"+b.Namespace)
	})
	var out [][]Commit
	var cur []Commit
	for _, c := range commits {
		if len(cur) > 0 && (cur[0].Provider != c.Provider || cur[0].Namespace != c.Namespace || len(cur) == batchSize) {
			out, cur = append(out, cur), nil
		}
		cur = append(cur, c)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

type syncCounts struct{ commits, stats, deletes, moves, deps, depsCleared, noRemote int }

// syncData sends what aline.team does not have yet and records when it tried and whether it worked.
// Callers hold the data lock.
func syncData(dir string, dryRun bool) (syncCounts, error) {
	n, err := sendPending(dir, dryRun)
	if dryRun {
		return n, err
	}
	st, lerr := loadSync(dir)
	if lerr != nil {
		return n, errors.Join(err, lerr)
	}
	st.LastSync, st.LastError = time.Now().Format(time.RFC3339), ""
	if err != nil {
		st.LastError = err.Error()
	}
	return n, errors.Join(err, saveSync(dir, st))
}

// sendPending sends queued deletions, then new or changed commits and snapshots (local mode: the
// snapshots first, the commits only if aline.team takes no snapshots yet). Progress is saved as it
// goes, so after a failure only what was not accepted is sent again.
// ponytail: the lock stays held while sending; other commands wait up to 30s. Send outside the lock
// if large first syncs make that a problem.
func sendPending(dir string, dryRun bool) (n syncCounts, err error) {
	c, err := newClient(dir)
	if err != nil {
		return n, err
	}
	if !c.loggedIn() { // logged out while this run waited or scanned: what is queued stays queued
		return n, failure("loginFirst")
	}
	st, err := loadSync(dir)
	if err != nil {
		return n, err
	}
	if st.Account != c.creds.Email || st.Server != c.creds.Server {
		if st.Account != "" { // another account or server: it has none of this device's records yet
			st = syncState{Commits: map[string]string{}, Changed: map[string]string{}, Deps: map[string]string{}, Stats: map[string]string{}}
		}
		st.Account, st.Server = c.creds.Email, c.creds.Server
	}
	if !dryRun {
		if n.moves, err = c.sendMoves(dir, &st); err != nil {
			return n, errors.Join(err, saveSync(dir, st))
		}
	} else if len(st.Moves) > 0 { // show the commits as the sync will, after the moves (not saved)
		out, err := buildExport(dir)
		if err != nil {
			return n, err
		}
		for _, mv := range st.Moves {
			renameSent(&st, mv, out.Commits)
		}
	}
	p, err := pending(dir, st)
	if err != nil {
		return n, err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return n, err
	}
	author := primaryEmail(cfg)
	n.noRemote = p.noRemote
	takes := time.Since(st.NoStats) >= 24*time.Hour // asked again a day after it said it takes none
	if !takes {
		p.Stats = nil
	}
	// Local mode sends the snapshots instead of commit records, but a server that takes no snapshots
	// yet still gets the commit records, as before local mode.
	local, held := cfg.AutoSyncOff && takes, p.Commits
	if local {
		p.Commits = nil
	}
	if dryRun { // the requests exactly as they would be sent
		known := sentRepos(st, p.Commits)
		for _, s := range p.Stats {
			known[s.Provider+"/"+s.Namespace] = true
		}
		out := struct {
			Moves         []repoMove    `json:"moves,omitempty"`
			Deletes       []deletion    `json:"deletes"`
			CommitBatches []commitBatch `json:"commitBatches"`
			Stats         []snapshot    `json:"stats"`
			Dependencies  []depsRequest `json:"dependencies,omitempty"`
		}{st.Moves, p.Deletes, []commitBatch{}, append([]snapshot{}, p.Stats...), p.depsChanged(st, known)}
		for _, b := range batches(p.Commits) {
			out.CommitBatches = append(out.CommitBatches, request(b, author, p.takeModified(b[0].Provider+"/"+b[0].Namespace)))
		}
		return n, printJSON(out)
	}
	save := func() error { return saveSync(dir, st) }

	for _, d := range p.Deletes {
		path := "/cli/repositories?" + url.Values{"provider": {d.Provider}, "namespace": {d.Namespace}}.Encode()
		if err := c.call("DELETE", path, nil, nil); err != nil {
			return n, errors.Join(err, save())
		}
		st.Deletes = slices.DeleteFunc(st.Deletes, func(x deletion) bool { return x == d })
		n.deletes++
	}
	st.Waiting = map[string]string{}
	lang, done := detectLang(os.Getenv), 0
	// sendStats sends the snapshots; one waiting for a verified email waits like the commits.
	sendStats := func() error {
		for _, s := range p.Stats {
			key := s.Provider + "/" + s.Namespace
			if _, ok := st.Waiting[key]; ok {
				continue
			}
			err := c.call("PUT", "/cli/stats", s, nil)
			var ae *apiError
			switch {
			case err == nil:
				n.stats++
			case errors.As(err, &ae) && ae.Code == codeUnverified:
				st.Waiting[key] = cmp.Or(strings.ToLower(ae.Email), "?")
				continue
			case errors.As(err, &ae) && ae.Code == codeBadInput: // not sent again on every push, until it changes
				warn(lang, "statsRejected", s.Namespace, ae)
			case errors.As(err, &ae) && ae.Code == codeNewerFormat: // all snapshots wait for the update
				warn(lang, "statsUpdate")
				return nil
			case unsupported(err): // all snapshots wait for a server that takes them
				st.NoStats, takes = time.Now(), false
				return save()
			default:
				return errors.Join(err, save())
			}
			st.Stats[key] = statsPrint(s) // saved with the rest; sent again after a crash, which changes nothing
		}
		return nil
	}
	if local {
		if err := sendStats(); err != nil {
			return n, err
		}
		if !takes {
			p.Commits = held
		}
	}
	defer progressDone()
	for _, batch := range batches(p.Commits) {
		progress(lang, "progressSend", done, len(p.Commits))
		done += len(batch)
		key := batch[0].Provider + "/" + batch[0].Namespace
		if _, ok := st.Waiting[key]; ok {
			continue // the repository's other batches wait too
		}
		err := c.upsert(author, batch, p.takeModified(key), func(x Commit, accepted bool) {
			st.Commits[commitKey(x)] = fingerprint(x)
			if accepted {
				n.commits++
			}
		}, func(sent []modifiedFile) {
			for _, f := range sent {
				st.Changed[key+"/"+f.Name] = f.ModifiedAt
			}
		})
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == codeUnverified { // only this repository waits: not marked sent
			st.Waiting[key], err = cmp.Or(strings.ToLower(ae.Email), "?"), nil
		}
		if serr := save(); err != nil || serr != nil {
			return n, errors.Join(err, serr)
		}
	}
	if !local { // after the commits, when there are some
		if err := sendStats(); err != nil {
			return n, err
		}
	}
	// Dependencies after the commits and snapshots: aline.team keeps them only for a repository it has.
	for _, req := range p.depsChanged(st, sentRepos(st, nil)) {
		if err := c.call("PUT", "/cli/dependencies", req, nil); err != nil {
			return n, errors.Join(err, save())
		}
		key := req.Provider + "/" + req.Namespace
		if len(req.Dependencies) == 0 {
			delete(st.Deps, key)
			n.depsCleared++
		} else {
			st.Deps[key] = depsPrint(req.Dependencies)
			n.deps++
		}
	}
	return n, save()
}

// sendMoves sends the queued address changes before any commits, so the new name does not start as a
// second repository. A move the server cannot make (or does not know, an older server) is dropped and
// the commits go again under the new name; offline or server trouble keeps it for the next sync.
func (c *client) sendMoves(dir string, st *syncState) (int, error) {
	if len(st.Moves) == 0 {
		return 0, nil
	}
	out, err := buildExport(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, mv := range slices.Clone(st.Moves) {
		var res struct {
			Result string `json:"result"` // MOVED, MERGED (into the new name's repository) or NOOP (nothing under the old name)
		}
		err := c.call("POST", "/cli/repositories/move", map[string]string{
			"provider": mv.From.Provider, "namespace": mv.From.Namespace,
			"newProvider": mv.To.Provider, "newNamespace": mv.To.Namespace,
		}, &res)
		var ae *apiError
		switch {
		case err == nil:
			renameSent(st, mv, out.Commits) // NOOP too: another device may have moved it already
			if res.Result != "NOOP" {
				n++
			}
		case errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 401 && ae.Status != 429:
		default:
			return n, err
		}
		st.Moves = slices.DeleteFunc(st.Moves, func(x repoMove) bool { return x == mv })
		if err := saveSync(dir, *st); err != nil {
			return n, err
		}
	}
	return n, nil
}

// upsert sends commits and calls done for each one the server has dealt with. A batch the server rejects
// as malformed (C001) is split to find the bad records; those are skipped with a warning and marked done
// (not accepted), so they are not sent on every push, until they change.
// The first changes in modified go with the first part of a split batch; if that part is rejected they
// are not marked sent and go with a later sync.
func (c *client) upsert(author string, commits []Commit, modified []modifiedFile, done func(c Commit, accepted bool), sent func([]modifiedFile)) error {
	err := c.call("POST", "/cli/commits/batch", request(commits, author, modified), nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == codeBadInput {
		if len(commits) == 1 {
			warn(detectLang(os.Getenv), "commitRejected", commits[0].Repo, commits[0].Hash[:min(12, len(commits[0].Hash))], ae)
			done(commits[0], false)
			return nil
		}
		h := len(commits) / 2
		if err := c.upsert(author, commits[:h], modified, done, sent); err != nil {
			return err
		}
		return c.upsert(author, commits[h:], nil, done, sent)
	}
	if err != nil {
		return err
	}
	for _, x := range commits {
		done(x, true)
	}
	sent(modified)
	return nil
}

// cmdSync collects new commits in every registered repository and sends what aline.team does not have.
func cmdSync(dir string, args []string) error {
	jsonMode(args)
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print what would be sent, send nothing")
	fs.Bool("json", false, "print the result as JSON")
	if err := parseFlags(fs, args, "gitfolio sync [--dry-run] [--json]"); err != nil {
		return err
	}
	lang := detectLang(os.Getenv)
	n, failed, err := syncNow(dir, lang, *dryRun)
	if err != nil || *dryRun {
		return err
	}
	return reportSync(dir, lang, n, failed)
}

// syncNow collects new commits in every repository and sends what aline.team does not have yet (with
// dryRun prints it instead); failed lists the repositories that could not be read. Callers hold the
// data lock.
func syncNow(dir, lang string, dryRun bool) (n syncCounts, failed []map[string]string, err error) {
	repos, err := loadRepos(dir)
	if err != nil {
		return n, nil, err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return n, nil, err
	}
	failed = []map[string]string{}
	rs := make([]*Repo, len(repos))
	for i := range repos {
		rs[i] = &repos[i]
	}
	progress(lang, "progressScan", 0, len(rs))
	err = scanRepos(dir, rs, false, func(i, _ int, err error) {
		defer progress(lang, "progressScan", i+1, len(rs))
		if err != nil {
			warn(lang, "repoFailed", rs[i].Name, err)
			failed = append(failed, map[string]string{"name": rs[i].Name, "error": err.Error()})
			return
		}
		if cfg.Deps && cfg.DepsAuto { // deps auto on: the daily sync runs this too
			if _, _, err := refreshDeps(dir, rs[i]); err != nil {
				warn(lang, "repoFailed", rs[i].Name, err)
			}
		}
	})
	progressDone() // before --dry-run prints its requests
	if err != nil {
		return n, failed, err
	}
	if err := saveRepos(dir, repos); err != nil {
		return n, failed, err
	}
	n, err = syncData(dir, dryRun)
	var ae *apiError
	if err != nil && (!errors.As(err, &ae) || ae.Status >= 500 || ae.Code == codeRateLimited) {
		return n, failed, failure("syncLater", err) // offline, server trouble or too many requests
	}
	return n, failed, err
}

// reportSync tells what one sync sent and what waits: as JSON with --json, else for people.
func reportSync(dir, lang string, n syncCounts, failed []map[string]string) error {
	st, err := loadSync(dir)
	if err != nil {
		return err
	}
	if jsonOut {
		waiting := []map[string]string{} // repositories aline.team takes once the email is verified
		for _, key := range slices.Sorted(maps.Keys(st.Waiting)) {
			_, namespace, _ := strings.Cut(key, "/")
			waiting = append(waiting, map[string]string{"namespace": namespace, "email": st.Waiting[key]})
		}
		return printJSON(map[string]any{"commits": n.commits, "deleted": n.deletes, "moved": n.moves, "stats": n.stats,
			"deps": n.deps, "depsCleared": n.depsCleared, "noRemote": n.noRemote, "waiting": waiting, "failed": failed})
	}
	switch {
	case n.commits+n.deletes > 0:
		say(lang, "synced", n.commits, n.deletes)
	case len(st.Waiting) == 0 && n.moves == 0 && n.stats+n.deps+n.depsCleared == 0:
		say(lang, "upToDate")
	}
	if n.stats > 0 {
		say(lang, "statsSent", n.stats)
	}
	if n.deps > 0 {
		say(lang, "depsSynced", n.deps)
	}
	if n.depsCleared > 0 {
		say(lang, "depsCleared", n.depsCleared)
	}
	if n.moves > 0 {
		say(lang, "repoMoved", n.moves)
	}
	if n.noRemote > 0 {
		say(lang, "noRemote", n.noRemote)
	}
	waitingNotice(lang, st.Waiting)
	return nil
}

// waitingNotice tells, once per email, which repositories aline.team takes only after it is verified.
func waitingNotice(lang string, waiting map[string]string) {
	byEmail := map[string][]string{}
	for key, email := range waiting {
		_, namespace, _ := strings.Cut(key, "/")
		byEmail[email] = append(byEmail[email], namespace)
	}
	for _, email := range slices.Sorted(maps.Keys(byEmail)) {
		warn(lang, "sendWaiting", email, strings.Join(slices.Sorted(slices.Values(byEmail[email])), ", "), email)
	}
}
