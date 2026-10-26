package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

type Repo struct {
	ID        string `json:"id"`
	Path      string `json:"path"` // local only, never exported or sent
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`  // GITHUB, GITLAB, ... or OTHER
	Namespace string `json:"namespace,omitempty"` // owner/repo on the git service
	LastScan  string `json:"last_scan,omitempty"`
	// Format is the version of this repository's stored commits; older ones are collected again (repoFormat).
	Format int `json:"format,omitempty"`
	// Seen fingerprints what the last scan read (scanPrint); the same again means nothing new to read.
	Seen string `json:"seen,omitempty"`
	// Changed maps each file the user created to when someone else first changed it (modifiedFiles).
	Changed map[string]string `json:"changed,omitempty"`
	// Churn and AsOf are what the last scan counted of everyone's history for the snapshot (churnOf).
	Churn map[string]churn `json:"churn,omitempty"`
	AsOf  string           `json:"asOf,omitempty"`
	// Manifests records the user's decision per package manager file path: true = may be read.
	// Local only, never exported or sent. Files missing here wait for approval.
	Manifests map[string]bool `json:"manifests,omitempty"`
	// AI is what AI coding tools may see of this repository through gitfolio mcp (config ai).
	// Local only, never sent.
	AI aiExposure `json:"ai,omitzero"`
}

// aiExposure is chosen by the user, or by their AI tool on their behalf.
type aiExposure struct {
	Name     string `json:"name,omitempty"`     // "": the repository's own name; anonymousName: a number; else this name
	Messages string `json:"messages,omitempty"` // "" (off), "subjects" or "full": what of the commit messages
	Branches bool   `json:"branches,omitempty"` // the branch names too
	Hidden   bool   `json:"hidden,omitempty"`   // not shown at all
}

const anonymousName = "-"

type FileStat struct {
	Name    string `json:"name"`              // path in the repository, as git diff and log show it (e.g. "cmd/gitfolio/git.go")
	Created bool   `json:"created,omitempty"` // this commit added the file (git status A; a rename is not)
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Module  string `json:"module,omitempty"` // local only: nearest approved manifest's directory, never exported
}

type Commit struct {
	Repo         string     `json:"repo,omitempty"`      // Repo.ID locally, repository name in export, left out when sent
	Provider     string     `json:"provider,omitempty"`  // filled in export only
	Namespace    string     `json:"namespace,omitempty"` // filled in export only
	Hash         string     `json:"hash"`
	Branch       string     `json:"branch,omitempty"`      // remote branch it is on (see remoteBranches), never masked
	AuthorEmail  string     `json:"authorEmail,omitempty"` // the real author; sent as the primary email
	Date         string     `json:"date"`
	Message      string     `json:"message"`
	Files        []FileStat `json:"files"`
	CreationType string     `json:"creationType"` // HUMAN, HUMAN_CO_AI or AI_CO_HUMAN
	AIAgents     []string   `json:"aiAgents,omitempty"`
	coAuthors    []string   // raw Co-authored-by emails, used for matching only, never stored
}

// repoFormat is the version of the stored commits: 3 also records which files a commit created, 2 file
// paths in the repository, 1 only file names.
const repoFormat = 3

func dataDir() (string, error) {
	dir, err := dataPath()
	if err != nil {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o700)
}

// dataPath is the data folder, without making it (dataDir does).
func dataPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	name := "gitfolio"
	if sourceBuild() { // a build from source keeps its own data, apart from the installed release
		name = "gitfolio-dev"
	}
	return filepath.Join(base, name), nil
}

func newRepo(path string) Repo {
	sum := sha256.Sum256([]byte(path))
	return Repo{ID: hex.EncodeToString(sum[:6]), Path: path, Name: filepath.Base(path)}
}

type Config struct {
	Mask   []string `json:"mask,omitempty"`   // blocked words: customer and internal project names
	Deps   bool     `json:"deps"`             // user allowed dependency detection (DESIGN 3.5)
	Roots  []string `json:"roots,omitempty"`  // folders where the user keeps repositories (init)
	APIURL string   `json:"apiUrl,omitempty"` // aline.team API root; empty = production (config api-url)
	// AutoSyncOff is local mode: nothing is sent after git push or daily, and a sync sends the
	// statistics only, no commit records (config autosync).
	AutoSyncOff bool `json:"autoSyncOff,omitempty"`
	// Emails are the user's work emails (gitfolio email); the first is the primary one, the author
	// email aline.team gets for all of the user's commits.
	Emails []string `json:"emails,omitempty"`
	// Schedule is the daily sync time ("09:00", local) registered with the OS scheduler; empty = none.
	Schedule string `json:"schedule,omitempty"`
	// GitHistoryOff stops keeping git's record of the commands gitfolio runs (history.go, config git-history).
	GitHistoryOff bool `json:"gitHistoryOff,omitempty"`
	// DepsAuto makes scan and sync, the daily one included, also read the approved package manager
	// files (deps auto); off, they are read only by deps scan and right after approving.
	DepsAuto bool `json:"depsAuto,omitempty"`
	// EmailsChecked is set once the user was asked to verify the work emails (init, or the one-time
	// question after updating to 0.2.0, when aline.team started to take commits only under verified emails).
	EmailsChecked bool `json:"emailsChecked,omitempty"`
	// LocalModeKnown is set once the user chose the sync mode (init, config autosync) or, with autosync
	// off from before 0.6.0, was told once that it is now local mode.
	LocalModeKnown bool `json:"localModeKnown,omitempty"`
	// MaskVersion is the maskVersion the stored messages were masked with.
	MaskVersion int `json:"maskVersion,omitempty"`
}

func loadRepos(dir string) (repos []Repo, err error) {
	err = loadJSON(filepath.Join(dir, "repos.json"), &repos)
	return repos, err
}

func saveRepos(dir string, repos []Repo) error {
	return saveJSON(filepath.Join(dir, "repos.json"), repos)
}

// loadConfig reads the settings. Before any are saved GitFolio is in local mode: it sends nothing by
// itself until the user turns automatic sync with aline.team on (init, config autosync on). Settings
// saved earlier keep what they say, automatic sync included.
func loadConfig(dir string) (cfg Config, err error) {
	p := filepath.Join(dir, "config.json")
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return Config{AutoSyncOff: true, LocalModeKnown: true}, nil
	}
	err = loadJSON(p, &cfg)
	return cfg, err
}

func saveConfig(dir string, cfg Config) error {
	return saveJSON(filepath.Join(dir, "config.json"), cfg)
}

// loadJSON decodes p into v; a missing file leaves v untouched.
func loadJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func saveJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p, b)
}

// writeFile replaces p in one step, so a reader sees the old file or the new one, never part of it. The
// temporary file has a name of its own: commands that write outside the lock (whoami saving a login, a
// history trim) never write into each other's.
func writeFile(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func readCommits(dir string) ([]Commit, error) {
	f, err := os.Open(filepath.Join(dir, "commits.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var commits []Commit
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 16<<20) // long commit messages
	for sc.Scan() {
		var c Commit
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, sc.Err()
}

func appendCommits(dir string, commits []Commit) error {
	return encodeJSONL(filepath.Join(dir, "commits.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, commits)
}

// writeCommits replaces commits.jsonl with commits.
func writeCommits(dir string, commits []Commit) error {
	p := filepath.Join(dir, "commits.jsonl")
	if err := encodeJSONL(p+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, commits); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func encodeJSONL[T any](p string, flag int, items []T) error {
	f, err := os.OpenFile(p, flag, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, c := range items {
		if err := enc.Encode(c); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// agentTag is what the post-commit hook records when an AI agent's environment made the commit.
type agentTag struct {
	Hash   string   `json:"hash"`
	Agents []string `json:"agents"`
}

func appendAgentTag(dir string, t agentTag) error {
	return encodeJSONL(filepath.Join(dir, "agent-tags.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, []agentTag{t})
}

// readAgentTags returns the recorded agents by commit hash.
// ponytail: the file only grows; prune tags of stored commits if it ever gets large.
func readAgentTags(dir string) (map[string][]string, error) {
	f, err := os.Open(filepath.Join(dir, "agent-tags.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tags := map[string][]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var t agentTag
		if json.Unmarshal(sc.Bytes(), &t) == nil { // skip a line cut short by a crash
			tags[t.Hash] = append(tags[t.Hash], t.Agents...)
		}
	}
	return tags, sc.Err()
}

// scanRepo stores the user's commits in r that are not stored yet and returns how many were added.
// With rebuild, r's stored commits are dropped first and collected again. Callers hold the data lock.
// ponytail: each scan lists every commit in range (no files) to find what is new; a repository whose
// commits changed has its files read again by name for churnOf and firstChanges. Keep what they count
// between scans and read only the new commits if big repositories get slow.
func scanRepo(dir string, r *Repo, rebuild bool) (int, error) {
	var n int
	var scanErr error
	if err := scanRepos(dir, []*Repo{r}, rebuild, func(_, added int, err error) { n, scanErr = added, err }); err != nil {
		return 0, err
	}
	return n, scanErr
}

// scanRepos scans rs like scanRepo. Their git is read side by side, the slow part, and what it finds is
// stored in order; each gets every repository's result before the next is stored. Callers hold the data lock.
func scanRepos(dir string, rs []*Repo, rebuild bool, each func(i, added int, err error)) error {
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if cfg.MaskVersion < maskVersion { // stricter masking reaches the messages stored before
		if _, err := maskStored(dir, newMasker(cfg.Mask)); err != nil {
			return err
		}
		cfg.MaskVersion = maskVersion
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
	}
	tags, err := readAgentTags(dir)
	if err != nil {
		return err
	}
	stored, err := readCommits(dir) // for readRepo to tell whether a repository has anything to store
	if err != nil {
		return err
	}
	had := storedNow{known: map[string]bool{}, noBranch: map[string]bool{}}
	for _, c := range stored {
		had.known[c.Hash] = true
		if c.Branch == "" {
			had.noBranch[c.Repo] = true
		}
	}
	reads := make([]chan repoRead, len(rs))
	for i := range reads {
		reads[i] = make(chan repoRead, 1)
	}
	go sideBySide(len(rs), func(i int) { reads[i] <- readRepo(cfg, tags, had, rs[i], rebuild) })
	for i, r := range rs {
		n, err := storeScan(dir, cfg, r, <-reads[i], rebuild)
		each(i, n, err)
	}
	return nil
}

// sideBySide runs f(0) to f(n-1) at the same time, as many at once as there are CPUs: for git commands
// on different repositories, each its own process.
func sideBySide(n int, f func(i int)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.NumCPU())
	for i := range n {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			f(i)
		})
	}
	wg.Wait()
}

// storedNow is what was stored when a scan started: the commit hashes, and the repositories with commits
// stored before branches were recorded.
type storedNow struct{ known, noBranch map[string]bool }

// repoRead is what a scan reads of a repository's git, which writes none of GitFolio's stored data.
type repoRead struct {
	err  error
	mine map[string]bool
	move *repoMove // a new address of the repository, for aline.team to rename what it has
	rng  []string  // nil: no commits yet
	seen string    // fingerprints what the history counts with (Repo.Seen)
	all  []Commit  // everyone's commits in rng, without their files
	need bool      // some of the user's commits are not stored, or are stored in an older format
}

// readRepo reads r's git for a scan and sets what it counts of the whole history on r.
func readRepo(cfg Config, tags map[string][]string, had storedNow, r *Repo, rebuild bool) (g repoRead) {
	if g.mine, g.err = myEmails(r.Path, cfg.Emails); g.err != nil {
		return g
	}
	if hooks, husky, err := hooksDir(r.Path); err == nil && husky {
		_ = huskyHooks(hooks, true) // husky rewrites its hook files on npm install; put gitfolio's lines back
	}
	r.Name = filepath.Base(r.Path)
	was := deletion{r.Provider, r.Namespace}
	if _, err := os.Stat(r.Path); err == nil { // a folder that is gone keeps its last address, for remove --purge
		r.Provider, r.Namespace = remote(r.Path)
	}
	// A new address of the same repository (git remote set-url, or its push URL now counts): aline.team
	// renames what it has rather than get the commits again under the new name.
	if now := (deletion{r.Provider, r.Namespace}); was.Namespace != "" && now.Namespace != "" && now != was {
		g.move = &repoMove{was, now}
	}
	if g.rng, g.err = logRange(r.Path); g.err != nil || g.rng == nil {
		return g
	}
	// Every commit in range, without its files: what is new, and whether the history counts the same.
	if g.all, g.err = readCommitsOf(r.Path, g.rng, false, tags); g.err != nil {
		return g
	}
	key := []any{g.rng, slices.Sorted(maps.Keys(g.mine)), version}
	for _, c := range g.all {
		key = append(key, c.Hash, c.AIAgents) // the agents also come from agent tags
		g.need = g.need || !had.known[c.Hash] && creationType(c, g.mine) != ""
	}
	g.seen = fingerprint(key)
	old := rebuild || r.Format < repoFormat
	g.need = g.need || old || g.rng[0] == "--remotes" && had.noBranch[r.ID]
	// The files of everyone's commits, the slow part, only when the commits or the rules changed.
	if old || g.seen != r.Seen {
		all, err := readCommitsOf(r.Path, g.rng, true, tags)
		if err != nil {
			g.err = err
			return g
		}
		r.Changed = firstChanges(all, g.mine)
		r.Churn, r.AsOf = nil, ""
		if g.rng[0] == "--remotes" { // the snapshot is of pushed history only
			r.Churn, r.AsOf = churnOf(all, g.mine)
		}
	}
	return g
}

// readCommitsOf parses gitLog of rng, the agents of agent tags added.
func readCommitsOf(repo string, rng []string, files bool, tags map[string][]string) ([]Commit, error) {
	out, err := gitLog(repo, rng, files)
	if err != nil {
		return nil, err
	}
	all := parseLog(out)
	for i, c := range all {
		if extra := tags[c.Hash]; len(extra) > 0 {
			all[i].AIAgents = slices.Compact(slices.Sorted(slices.Values(append(c.AIAgents, extra...))))
		}
	}
	return all, nil
}

// storeScan stores the user's commits that readRepo found and are not stored yet.
func storeScan(dir string, cfg Config, r *Repo, g repoRead, rebuild bool) (int, error) {
	if g.move != nil {
		if err := queueMove(dir, *g.move); err != nil {
			return 0, err
		}
	}
	if g.err != nil || g.rng == nil {
		return 0, g.err
	}
	if !g.need {
		r.LastScan, r.Seen = time.Now().Format(time.RFC3339), g.seen
		return 0, nil
	}
	mine, rng, all, m := g.mine, g.rng, g.all, newMasker(cfg.Mask)
	var modules map[string]string
	if cfg.Deps { // module IDs only: package manager files are read by `deps scan`, not here
		modules = manifestModules(*r)
	}
	stored, err := readCommits(dir)
	if err != nil {
		return 0, err
	}
	// Commits stored before branches or file paths were recorded are collected once more.
	if r.Format < repoFormat || (rng[0] == "--remotes" && slices.ContainsFunc(stored, func(c Commit) bool { return c.Repo == r.ID && c.Branch == "" })) {
		rebuild = true
	}
	if rebuild {
		stored = slices.DeleteFunc(stored, func(c Commit) bool { return c.Repo == r.ID })
		if err := writeCommits(dir, stored); err != nil {
			return 0, err
		}
	}
	known := map[string]bool{}
	for _, c := range stored {
		known[c.Hash] = true
	}
	var fresh []Commit
	var hashes []string
	for _, c := range all {
		if known[c.Hash] {
			continue
		}
		if c.CreationType = creationType(c, mine); c.CreationType != "" {
			c.Repo = r.ID
			m.commit(&c) // after AI detection in parseLog, before anything is written
			fresh, hashes = append(fresh, c), append(hashes, c.Hash)
			known[c.Hash] = true
		}
	}
	stats, err := fileStats(r.Path, hashes)
	if err != nil {
		return 0, err
	}
	for i := range fresh {
		fresh[i].Files = stats[fresh[i].Hash]
		for j := range fresh[i].Files {
			fresh[i].Files[j].Module = moduleOf(fresh[i].Files[j].Name, modules)
		}
	}
	if len(fresh) > 0 && rng[0] == "--remotes" {
		branches, err := remoteBranches(r.Path)
		if err != nil {
			return 0, err
		}
		for i := range fresh {
			fresh[i].Branch = branches[fresh[i].Hash] // as is, so aline.team matches it with its GitHub sync
		}
	}
	if err := appendCommits(dir, fresh); err != nil {
		return 0, err
	}
	r.LastScan, r.Format, r.Seen = time.Now().Format(time.RFC3339), repoFormat, g.seen
	return len(fresh), nil
}
