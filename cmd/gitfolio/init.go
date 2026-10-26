package main

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

// logo is ASCII only so it renders in any terminal font, including Windows consoles (DESIGN 7.2).
const logo = `    _     _      ___  _   _  _____  _____  _____     _     __  __
   / \   | |    |_ _|| \ | || ____||_   _|| ____|   / \   |  \/  |
  / _ \  | |     | | |  \| ||  _|    | |  |  _|    / _ \  | |\/| |
 / ___ \ | |___  | | | |\  || |___   | |  | |___  / ___ \ | |  | |
/_/   \_\|_____||___||_| \_||_____|  |_|  |_____|/_/   \_\|_|  |_|
 =================================================================
`

// header is the logo with version and license; shown by init and version only, never by hooks.
func header() string {
	return indent(logo + fmt.Sprintf(" :: GitFolio :: %48s\n", "("+version+")") +
		" Apache License 2.0 - Copyright (c) 2026 Alineteam Inc.\n")
}

// Credentials live in their own file with owner-only permissions. Nothing here is ever printed.
type Credentials struct {
	Token          string `json:"token,omitempty"` // aline.team CLI token, sent only in the Authorization header
	TokenExpiresAt string `json:"tokenExpiresAt,omitempty"`
	Email          string `json:"email,omitempty"` // account email, shown by login
	// Verified are the emails aline.team verified for this account: the account email and the work
	// emails verified with a code (DESIGN 6.1.2), lowercased, as of the last answer from the server.
	Verified []string `json:"verifiedEmails,omitempty"`
	// Server is the aline.team API that issued the token. A token is only sent there: a build that talks
	// to another server (a release build always uses production) counts as logged out. Empty before 0.2.1.
	Server string `json:"server,omitempty"`
}

func loadCredentials(dir string) (c Credentials, err error) {
	err = loadJSON(filepath.Join(dir, "credentials.json"), &c)
	return c, err
}

func saveCredentials(dir string, c Credentials) error {
	return saveJSON(filepath.Join(dir, "credentials.json"), c) // 0600
}

// cmdInit walks a new user through setup (DESIGN 7.2): login, the repositories to collect, the
// settings, then a first sync. Nothing after the login step runs until the user is logged in to
// aline.team. Every question defaults to the current setting, so running init again changes only
// what the user changes.
func cmdInit(dir string, args []string) error {
	jsonMode(args)
	lang := detectLang(os.Getenv)
	switch {
	case slices.Equal(withoutJSON(args), []string{"--policy"}):
		if jsonOut { // for a program to show first, as init does
			return printJSON(map[string]string{"policy": tr(lang, "policy"), "setupActions": setupActions(lang, dir)})
		}
		fmt.Fprint(out, indent(tr(lang, "policy")))
		return nil
	case slices.Contains(args, "--scan"):
		return initScan(dir, args)
	case jsonOut:
		return failure("usage", initUsage)
	}
	fmt.Fprint(out, header()+"\n"+indent(tr(lang, "policy"))+"\n")
	if interactive() && (!askYesNo(lang, "policyAgree", true) || stdinClosed) {
		say(lang, "policyDeclined")
		return nil
	}
	if err := cmdLogin(dir, nil); err != nil {
		return err
	}
	c, err := newClient(dir)
	if err != nil || !c.loggedIn() {
		return err // sign-up declined: nothing is set up before login
	}
	_, _ = c.me() // verified emails, for an account logged in before they were kept here
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}

	// Repositories: where they are, whose commits count, which ones to collect.
	section(lang, "reposTitle")
	roots := askRoots(lang, cfg.Roots, args)
	msg := tr(lang, "searching")
	if runtime.GOOS == "darwin" {
		msg += tr(lang, "searchingMac")
	}
	notice(msg) // still working: a note, not a result

	found := findRepos(roots, 5) // once: for the emails, then for the repositories
	// Whose commits count: the emails git uses in these repositories, as the user picks them; more can be
	// added here or later with `gitfolio email add`.
	cfg.Emails = chooseEmails(lang, c.creds, cfg.Emails, found)
	if len(cfg.Emails) > 0 {
		notice(tr(lang, "identityWork"))
		notice(emailList(lang, cfg, c.creds))
	}
	for interactive() && !stdinClosed {
		answer := prompt(tr(lang, "emailMoreAsk"))
		bad := addEmails(&cfg, strings.Split(answer, ","))
		if len(bad) == 0 {
			break
		}
		notice(fmt.Sprintf(tr(lang, "emailBad"), strings.Join(bad, ", ")) + "\n")
	}
	if err := withLock(dir, func() error {
		c, err := loadConfig(dir)
		if err != nil {
			return err
		}
		c.Roots, c.Emails, c.EmailsChecked = roots, cfg.Emails, true
		return saveConfig(dir, c)
	}); err != nil {
		return err
	}
	// aline.team takes commits only under verified emails and merges the repositories linked on the web
	// under them (DESIGN 6.1.2): each picked email is verified now, one after the other.
	if interactive() && slices.ContainsFunc(cfg.Emails, func(e string) bool { return !verified(c.creds, e) && !noreply(e) }) {
		notice(tr(lang, "emailVerifyNotice"))
		verifyEmails(lang, c, cfg.Emails)
	}
	chosen, err := chooseRepos(lang, dir, found, cfg.Emails)
	if err != nil {
		return err
	}
	ai := aiExposure{}
	if len(chosen) > 0 && interactive() {
		ai = askRepoChoices(lang)
	}

	// Settings. Syncing automatically sends right after git push (config autosync) and, when scheduled,
	// every day; local mode sends nothing by itself, only when a command needs aline.team's answer.
	section(lang, "settingsTitle")
	notice(tr(lang, "syncModeNotice"))
	auto := askYesNo(lang, "syncModeAsk", !cfg.AutoSyncOff)
	notice(tr(lang, "depsNotice"))
	deps := askYesNo(lang, "depsAsk", cfg.Deps)
	when := ""
	if auto {
		when = askSchedule(lang, cfg.Schedule)
	}
	blank()
	if err := withLock(dir, func() error {
		c, err := loadConfig(dir)
		if err != nil {
			return err
		}
		c.AutoSyncOff, c.LocalModeKnown = !auto, true
		return saveConfig(dir, c)
	}); err != nil {
		return err
	}
	if err := withLock(dir, func() error { return applyDeps(dir, deps) }); err != nil {
		return err
	}
	if when != cfg.Schedule {
		if err := withLock(dir, func() error { return setSchedule(dir, lang, when) }); err != nil {
			warn(lang, "failed", err) // the rest of the setup still stands
		}
	}
	if len(chosen) > 0 {
		blank()
	}
	// The package manager files of all chosen repositories are asked about together, not one by one.
	rs := make([]Repo, len(chosen))
	widest := 0
	for i, p := range chosen {
		rs[i] = newRepo(p)
		rs[i].AI = ai
		widest = max(widest, width(rs[i].Name))
	}
	if deps && interactive() {
		reviewManifests(rs)
	}
	var done []string // one table of what was registered, not a line per repository
	for i, r := range rs {
		progress(lang, "progressRegister", i, len(rs))
		var n int
		if err := withLock(dir, func() (err error) { n, err = registerRepo(dir, r, false); return err }); err != nil {
			warn(lang, "repoFailed", tildePath(r.Path), err)
			continue
		}
		done = append(done, fmt.Sprintf("  %s  %6d %s", pad(r.Name, widest), n, dim(tr(lang, "commitsUnit"))))
	}
	if len(done) > 0 {
		say(lang, "registeredMany", len(done))
		show(out, strings.Join(done, "\n"))
	}

	// Sync (the first one sends the history of the chosen repositories), then what was set up.
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	if len(repos) > 0 && auto {
		section(lang, "syncTitle")
		if err := withLock(dir, func() error { return cmdSync(dir, nil) }); err != nil {
			warn(lang, "failed", err) // sent by the next push or sync
		}
	}
	if cfg, err = loadConfig(dir); err != nil {
		return err
	}
	onOff := func(b bool) string { return tr(lang, map[bool]string{true: "on", false: "off"}[b]) }
	daily := cmp.Or(cfg.Schedule, tr(lang, "off"))
	blank()
	sayKV(lang, "initDone", len(repos), primaryEmail(cfg), tr(lang, map[bool]string{true: "syncAuto", false: "syncLocal"}[!cfg.AutoSyncOff]), onOff(cfg.Deps), daily)
	return nil
}

const initUsage = "gitfolio init [folder...] | init --policy [--json] | init --scan [--root <folder>]... --json"

// initScan is init's search for a program, asking nothing; the program shows the results and sets up
// what the user picks with email, add, config autosync, deps and schedule. It gives the folders searched
// (--root, else the ones init saved, the usual code folders or the home folder), every repository found
// with the user's commits in it, and the emails git uses in them. Only after logging in, as init.
func initScan(dir string, args []string) error {
	var roots []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--scan" || args[i] == "--json":
		case args[i] == "--root" && i+1 < len(args):
			i++
			p, err := filepath.Abs(expandHome(args[i]))
			if st, e := os.Stat(p); err != nil || e != nil || !st.IsDir() {
				return failure("rootMissing", args[i])
			}
			roots = append(roots, p)
		default:
			return failure("usage", initUsage)
		}
	}
	if !jsonOut {
		return failure("usage", initUsage)
	}
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	if !c.loggedIn() {
		return failure("loginFirst")
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if len(roots) > 0 { // kept for the next time, as init does
		if err := withLock(dir, func() error {
			c, err := loadConfig(dir)
			if err != nil {
				return err
			}
			c.Roots = roots
			return saveConfig(dir, c)
		}); err != nil {
			return err
		}
	} else if roots = cfg.Roots; len(roots) == 0 {
		home, _ := os.UserHomeDir()
		if roots = suggestRoots(home); len(roots) == 0 {
			roots = []string{home}
		}
	}
	found := findRepos(roots, 5)
	emails, uses := repoEmails(found)
	work := cfg.Emails // whose commits count: the work emails, or before there are any, all the emails found
	if len(work) == 0 {
		work = emails
	}
	registered, err := loadRepos(dir)
	if err != nil {
		return err
	}
	type repo struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		OwnCommits int    `json:"ownCommits"`
		LastCommit string `json:"lastCommit,omitempty"` // of the user's own commits
		Registered bool   `json:"registered"`
	}
	repos := []repo{}
	for _, p := range found {
		n, last := ownCommits(p, work)
		repos = append(repos, repo{filepath.Base(p), p, n, last, slices.ContainsFunc(registered, func(r Repo) bool { return r.Path == p })})
	}
	list := []map[string]any{}
	for _, e := range append(slices.Clone(cfg.Emails), emails...) {
		if slices.ContainsFunc(list, func(m map[string]any) bool { return m["email"] == e }) {
			continue
		}
		list = append(list, map[string]any{"email": e, "repositories": uses[e], "work": slices.Contains(cfg.Emails, e),
			"verified": verified(c.creds, e), "noreply": noreply(e)})
	}
	return printJSON(map[string]any{"roots": roots, "repositories": repos, "emails": list})
}

// repoEmails returns the emails git uses for commits in repos, with how many of them use each: the
// global user.email first, then each repository's own (its local config or a conditional include).
// These are the user's own settings, not commit authors, so teammates' emails do not show up.
func repoEmails(repos []string) (emails []string, uses map[string]int) {
	uses = map[string]int{}
	add := func(e string, n int) {
		if e = strings.ToLower(strings.TrimSpace(e)); validEmail(e) {
			if _, ok := uses[e]; !ok {
				emails = append(emails, e)
			}
			uses[e] += n
		}
	}
	if out, err := git(".", "config", "--global", "user.email"); err == nil {
		add(out, 0)
	}
	for _, r := range repos {
		if out, err := git(r, "config", "user.email"); err == nil {
			add(out, 1)
		}
	}
	return emails, uses
}

// chooseEmails lists the work emails so far and the emails git uses in repos, and asks which are the
// user's. Enter takes them all: they come from the user's own git settings. The emails so far stay first,
// so the primary does not change. Without a terminal it keeps them, or takes the global git email.
func chooseEmails(lang string, creds Credentials, have, repos []string) []string {
	found, uses := repoEmails(repos)
	list := slices.Clone(have)
	for _, e := range found {
		if !slices.Contains(list, e) {
			list = append(list, e)
		}
	}
	if len(list) == 0 {
		notice(tr(lang, "identityMissing"))
		return nil
	}
	if !interactive() {
		return list[:max(len(have), 1)]
	}
	notice(tr(lang, "emailCandidates"))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, e := range list {
		state := ""
		switch {
		case noreply(e):
			state = "noreply"
		case verified(creds, e):
			state = symDone
		}
		fmt.Fprintf(w, "%s  %d\t%s\t%s\t%s\n", margin, i+1, e, fmt.Sprintf(tr(lang, "emailRepoCount"), uses[e]), state)
	}
	w.Flush()
	for !stdinClosed {
		answer := prompt(tr(lang, "emailSelect"))
		if answer == "" {
			return list
		}
		sel, err := parseSelection(answer, len(list))
		if err == nil {
			var picked []string
			for _, i := range sel {
				picked = append(picked, list[i-1])
			}
			return picked
		}
		notice(fmt.Sprintf(tr(lang, "badSelection"), len(list)))
	}
	return have
}

// chooseRepos lists the unregistered repositories with the user's commits and asks which to collect.
// Nothing is chosen by default: company code is never collected unless the user picks it.
func chooseRepos(lang, dir string, found, work []string) ([]string, error) {
	repos, err := loadRepos(dir)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, r := range repos {
		registered[r.Path] = true
	}
	type candidate struct {
		path, last string
		commits    int
	}
	var cands []candidate
	for _, p := range found {
		if n, last := ownCommits(p, work); n > 0 && !registered[p] {
			cands = append(cands, candidate{p, last, n})
		}
	}
	if len(cands) == 0 {
		say(lang, "noCandidates")
		return nil, nil
	}
	blank()
	// Columns padded by display width (Korean and Japanese headers take two columns per character).
	head := strings.Split(tr(lang, "reposColumns"), "\t")
	wPath, wNum := width(head[0]), width(head[1])
	for _, c := range cands {
		wPath, wNum = max(wPath, width(tildePath(c.path))), max(wNum, len(strconv.Itoa(c.commits)))
	}
	wIdx := len(strconv.Itoa(len(cands)))
	fmt.Fprintf(out, "%s  %s  %s  %s  %s\n", margin, dim(pad("#", wIdx)), dim(pad(head[0], wPath)), dim(pad(head[1], wNum)), dim(head[2]))
	for i, c := range cands {
		fmt.Fprintf(out, "%s  %*d  %s  %*d  %s\n", margin, wIdx, i+1, pad(tildePath(c.path), wPath), wNum, c.commits, c.last)
	}
	notice(tr(lang, "companyNotice"))
	var sel []int
	for interactive() && !stdinClosed {
		answer := prompt(tr(lang, "selectRepos"))
		if answer == "" {
			break
		}
		if sel, err = parseSelection(answer, len(cands)); err == nil {
			break
		}
		notice(fmt.Sprintf(tr(lang, "badSelection"), len(cands)))
	}
	if len(sel) == 0 {
		if stdinClosed || !interactive() {
			say(lang, "chooseLater")
		} else {
			say(lang, "noneSelected")
		}
		return nil, nil
	}
	var chosen []string
	for _, i := range sel {
		chosen = append(chosen, cands[i-1].path)
	}
	return chosen, nil
}

// applyDeps saves the dependency detection choice. Turning it on asks about the files of repositories
// already registered; turning it off deletes what it collected. Callers hold the data lock.
func applyDeps(dir string, deps bool) error {
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	was := cfg.Deps
	cfg.Deps = deps
	if err := saveConfig(dir, cfg); err != nil {
		return err
	}
	switch {
	case deps && !was:
		repos, err := loadRepos(dir)
		if err != nil {
			return err
		}
		if err := reviewAndRescan(dir, repos); err != nil {
			return err
		}
		return saveRepos(dir, repos)
	case !deps && was:
		return cmdDeps(dir, []string{"off"})
	}
	return nil
}

// askYesNo asks a yes/no question; Enter keeps def.
func askYesNo(lang, key string, def bool, args ...any) bool {
	hint := map[bool]string{true: " [Y/n] > ", false: " [y/N] > "}[def]
	for {
		switch a := strings.ToLower(prompt(fmt.Sprintf(tr(lang, key), args...) + hint)); {
		case a == "" || stdinClosed:
			return def
		case a == "y" || a == "yes":
			return true
		case a == "n" || a == "no":
			return false
		}
		notice(tr(lang, "yesNoAgain"))
	}
}

// askRepoChoices says that the chosen repositories get git hooks, then asks once for all of them what AI
// coding tools see. Each answer defaults to the usual one; config ai changes it later, per repository.
func askRepoChoices(lang string) (ai aiExposure) {
	notice(tr(lang, "hooksNotice"))
	switch askChoice(lang, "aiNameAsk", 3, 0) {
	case 1:
		ai.Name = anonymousName
	case 2:
		return aiExposure{Hidden: true}
	}
	detail := askChoice(lang, "aiDetailAsk", 3, 0)
	ai.Messages, ai.Branches = []string{"", "subjects", "full"}[detail], detail > 0
	return ai
}

// askChoice asks to pick one of n numbered answers in key's text; Enter keeps def (0-based).
func askChoice(lang, key string, n, def int) int {
	for {
		a := strings.TrimSpace(prompt(tr(lang, key) + fmt.Sprintf(" [%d] > ", def+1)))
		if a == "" || stdinClosed {
			return def
		}
		if i, err := strconv.Atoi(a); err == nil && i >= 1 && i <= n {
			return i - 1
		}
		notice(fmt.Sprintf(tr(lang, "choiceAgain"), n))
	}
}

// askSchedule asks for the daily sync time: Enter keeps the current one (none at first), off removes it.
func askSchedule(lang, current string) string {
	hint := tr(lang, "scheduleAskNew")
	if current != "" {
		hint = fmt.Sprintf(tr(lang, "scheduleAskKeep"), current)
	}
	for {
		a := strings.ToLower(prompt(tr(lang, "scheduleAsk") + hint))
		if a == "" || stdinClosed {
			return current
		}
		if a == "off" || a == "n" || a == "no" {
			return ""
		}
		if h, m, ok := parseHHMM(a); ok {
			return fmt.Sprintf("%02d:%02d", h, m)
		}
		notice(tr(lang, "scheduleFormat"))
	}
}

// rootHints are common places to keep code, relative to the home folder.
var rootHints = []string{"Code", "code", "dev", "Dev", "src", "projects", "Projects", "workspace", "Workspace",
	"repos", "git", "GitHub", "IdeaProjects", "Documents/Code", "Documents/GitHub", "Documents/Projects", "Documents/dev"}

// suggestRoots returns the rootHints that exist under home, once each even on case-insensitive disks.
func suggestRoots(home string) []string {
	var found []string
	var infos []os.FileInfo
	for _, h := range rootHints {
		p := filepath.Join(home, filepath.FromSlash(h))
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() || slices.ContainsFunc(infos, func(o os.FileInfo) bool { return os.SameFile(o, st) }) {
			continue
		}
		infos = append(infos, st)
		found = append(found, p)
	}
	return found
}

// askRoots asks where the user keeps repositories. Folders given on the command line win; otherwise
// the saved or suggested folders are offered, and the whole home folder is the last resort.
func askRoots(lang string, saved, args []string) []string {
	home, _ := os.UserHomeDir()
	if len(args) > 0 {
		var roots []string
		for _, a := range args {
			if p, err := filepath.Abs(expandHome(a)); err == nil {
				roots = append(roots, p)
			}
		}
		return roots
	}
	defaults := saved
	if len(defaults) == 0 {
		defaults = suggestRoots(home)
	}
	fallback := defaults
	if len(fallback) == 0 {
		fallback = []string{home}
	}
	if !interactive() {
		return fallback
	}
	q := tr(lang, "rootsAsk")
	if len(defaults) > 0 {
		var shown []string
		for _, d := range defaults {
			shown = append(shown, tildePath(d))
		}
		q += fmt.Sprintf(tr(lang, "rootsFound"), strings.Join(shown, ", "))
	}
	q += tr(lang, "rootsPrompt")
	for {
		answer := prompt(q)
		if answer == "" {
			return fallback
		}
		var roots []string
		missing := ""
		for _, a := range strings.Split(answer, ",") {
			p, err := filepath.Abs(expandHome(strings.TrimSpace(a)))
			if st, e := os.Stat(p); err != nil || e != nil || !st.IsDir() {
				missing = strings.TrimSpace(a)
				break
			}
			roots = append(roots, p)
		}
		if missing == "" {
			return roots
		}
		notice(fmt.Sprintf(tr(lang, "rootMissing"), missing))
	}
}

func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}

// findRepos walks each root up to maxDepth folders deep and returns the git top-level of every
// repository found, once per repository even when it has several worktrees. It never descends into
// a repository (nested repositories and submodules are not offered), hidden folders, Library,
// node_modules or vendor, and only looks at folder names, not file contents.
func findRepos(roots []string, maxDepth int) []string {
	seen := map[string]bool{}
	var repos []string
	for _, root := range roots {
		root = filepath.Clean(root)
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil // unreadable folders (e.g. macOS privacy prompts declined) are skipped
			}
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "Library" || name == "node_modules" || name == "vendor") {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
				common, err := git(p, "rev-parse", "--path-format=absolute", "--git-common-dir")
				top, err2 := topLevel(p)
				if err == nil && err2 == nil && !seen[strings.TrimSpace(common)] {
					seen[strings.TrimSpace(common)] = true
					repos = append(repos, top)
				}
				return fs.SkipDir
			}
			if rel, err := filepath.Rel(root, p); err == nil && rel != "." && strings.Count(rel, string(filepath.Separator))+1 >= maxDepth {
				return fs.SkipDir
			}
			return nil
		})
	}
	return repos
}

// ownCommits counts the user's commits in repo over the same range scan reads, and returns the date
// of the latest one (YYYY-MM-DD).
func ownCommits(repo string, work []string) (n int, last string) {
	mine, err := myEmails(repo, work)
	if err != nil {
		return 0, ""
	}
	rng, err := logRange(repo)
	if err != nil || rng == nil {
		return 0, ""
	}
	out, err := git(repo, append([]string{"log", "--no-merges", "--format=%aE %as"}, rng...)...)
	if err != nil {
		return 0, ""
	}
	for _, line := range strings.Split(out, "\n") {
		if email, date, ok := strings.Cut(line, " "); ok && mine[strings.ToLower(email)] {
			n++
			last = max(last, date)
		}
	}
	return n, last
}
