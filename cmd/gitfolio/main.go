package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// version is set at release time via -ldflags "-X main.version=...". A build from source carries the
// release it leads to, with "-dev" (sourceBuild).
var version = "0.7.1-dev"

// sourceBuild reports whether this is a build from source: version "dev" or a release number with
// "-dev". It keeps its own data folder (dataDir), may talk to another server (apiBase), registers its own
// daily sync (fromSource) and never updates itself.
func sourceBuild() bool { return version == "dev" || strings.HasSuffix(version, "-dev") }

const usage = `usage: gitfolio <command> [arguments]

commands:
  init [folder...]      first-time setup: data policy, aline.team log in / sign up, then find the repositories
                        under your code folders and choose which ones to collect (without git, it offers
                        to install it first)
  init --policy [--json] | init --scan [--root <folder>]... --json
                        init's steps for a program, without questions: the data policy and what
                        starting GitFolio does, then (logged in) the repositories and emails found;
                        email, add, deps, config autosync and schedule set up what the user picks
  login                 log in / sign up to aline.team with an email code (an email without an
                        account is signed up)
  login --email <email> [--code <code> [--signup [--notify]]] [--json]
                        the same without questions, in two runs: --email sends the code, --code
                        finishes (--signup: agree to sign up an email without an account,
                        --notify: also receive aline.team service notifications by email)
  logout [--json]       sign out: revoke and remove this device's token
  whoami [--json]       show the aline.team account this device is signed in to
  add [path] [--json]   register a repository, collect its commits and install git hooks
                        (post-commit, pre-push) so later pushes are collected automatically
  remove [path] [--purge] [--json]
                        unregister a repository and restore its previous hooks
                        (--purge: also delete its collected commits, here and on aline.team)
  scan [path] [--all] [--rebuild] [--json]
                        collect new commits (--all: every registered repository,
                        --rebuild: drop stored commits and collect again)
  sync [--dry-run] [--json]
                        collect new commits in every repository and send what aline.team
                        does not have yet (--dry-run: print it as JSON, send nothing;
                        --json: what was sent and what waits, as JSON).
                        Pushes are sent right after git push unless autosync is off;
                        in local mode (autosync off) only the statistics are sent
  list                  show registered repositories
  status [name|path] [--json]
                        what GitFolio collected per repository: commits, how many aline.team has,
                        file changes, lines, AI use, languages, dependencies (this computer's data only)
  status --json         without a name: the account, sync mode, last sync, commits not sent yet and
                        each repository, as JSON for programs (this computer's data only)
  stats [--refresh] [--json]
                        your developer type and its five scores from aline.team, saved here after the
                        first time (--refresh: get it again; aline.team makes it if it has none yet,
                        --json: as JSON, for programs). In local mode it sends the statistics first
                        and gets the type each time
  stack [--json]        how long you used each language, framework and library, worked out on this
                        computer from the registered repositories as aline.team does (no login needed;
                        --json: as JSON, for programs)
  portfolio [file.md|file.pdf] [--from file.md]
                        write your portfolio from your git history: developer type, tech stack and
                        repositories, as Markdown to edit or a PDF printed with Chrome, Edge or
                        Chromium (no file named: it asks which); --from prints your edited Markdown
  mcp                   serve GitFolio to AI coding tools (Claude Code, Codex, ...) as an MCP server
                        on stdin and stdout: your work, type, stack and portfolio as config ai allows,
                        and setting GitFolio up with your email (nothing is removed through it)
  export [--json]       print collected commits, dependencies and statistics as JSON
  deps [on|off|review [path] [--allow <file>]...] [--json]
                        dependency detection: show status, turn it on (you choose which
                        package manager files may be read) or off (collected dependencies
                        are deleted), or change the file choices of a repository (--allow,
                        without questions: the files that may be read; the others are declined)
  deps scan [path] [--all]
                        read the approved package manager files again to update the
                        dependencies
  deps auto on|off      scan, sync and the daily sync also read the approved package
                        manager files (off by default; pushes never read them)
  config [--json]       show settings (--json after a change below: the settings)
  config mask add|rm <word>...
                        add or remove blocked words (customer or internal project names);
                        added words are also applied to already stored data
  config autosync on|off
                        on: sync with aline.team right after each git push; off (the default):
                        local mode, where only stats, portfolio and sync call the aline.team API, and
                        the daily sync is removed
  config git-history on|off
                        keep git's own record of the git commands gitfolio runs (on by default;
                        off deletes it)
  config ai [name <repo> real|anonymous|<name> | messages <repo> off|subjects|full |
            branches <repo> on|off | hide|show <repo>] [--json]
                        what AI coding tools see of each repository through gitfolio mcp: its real
                        name (default), anonymous or another name; commit messages and branch names
                        (off by default); or nothing. Paths and file names are never shown
                        (--json: every repository's settings, after a change too)
  history [--all] [--json]
                        the git commands gitfolio ran, newest last (--all: every run kept)
  email [add <email>... | verify <email> [--code <code>] | rm <email> | primary <email>] [--json]
                        your work emails: commits by any of them count as yours, and aline.team
                        gets all of them under the primary one (the first, marked ★); verified
                        ones (✔, email code) merge the repositories linked on aline.team with them;
                        without a terminal, verify sends the code and verify --code finishes
  schedule [HH:MM | off] [--json]
                        optional daily sync at a local time, for pushes the hooks missed
                        (launchd on macOS, systemd or cron on Linux); no argument: show it
  update [--path <gitfolio>] [--json]
                        install the newest release now (otherwise GitFolio offers it once a day), the
                        way it was installed; --path: of the gitfolio there instead of this one
  uninstall [--purge-local] [--json]
                        before removing GitFolio: unregister every repository and put back its earlier
                        hooks, remove the daily sync and log out (--purge-local: also delete the data on
                        this computer). Nothing on aline.team is deleted
  git [install | update] [--json]
                        whether git is installed (path, version), or install it with the system's
                        installer (at the terminal after asking, as init does), or update it the way
                        it was installed (Homebrew, winget; Apple's: opens Software Update)
  version [--json]      print version (--json: also the release number, whether it is a build from source
                        or a copy an app ships and updates, and the language of GitFolio's texts)

--json, where a command takes it, prints one JSON document on stdout for programs, a failure too
({"error": {"code", "message"}}, exit status 1); messages for people go to stderr, and so does the
progress of a long run, one JSON line per step: {"progress": {"step", "text", "done", "total"}}.`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if jsonOut {
			_ = printJSON(map[string]any{"error": errorJSON(err)})
		} else {
			warn(detectLang(os.Getenv), "failed", err)
		}
		os.Exit(1)
	}
}

// jsonOut is set by --json: stdout then carries one JSON document for programs,
// a failure too (main), and what people read goes to stderr.
var jsonOut bool

func jsonMode(args []string) {
	if slices.Contains(args, "--json") {
		jsonOut, out = true, errOut
	}
}

// withoutJSON is args without --json, for commands that read the rest by position.
func withoutJSON(args []string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--json" })
}

// errorCode is what programs reading --json branch on: the message key of a translated error, a name
// for the aline.team answers a program acts on (serverCodes), failed for anything else.
func errorCode(err error) string {
	var f *failErr
	var ae *apiError
	if errors.As(err, &f) {
		return f.key
	}
	if errors.As(err, &ae) && serverCodes[ae.Code] != "" {
		return serverCodes[ae.Code]
	}
	return "failed"
}

// errorJSON is the {"error": ...} a failure prints with --json: its code, message and extra texts.
func errorJSON(err error) map[string]string {
	e := map[string]string{"code": errorCode(err), "message": err.Error()}
	var f *failErr
	if errors.As(err, &f) {
		maps.Copy(e, f.extra)
	}
	return e
}

var serverCodes = map[string]string{
	codeTokenInvalid: "loginFirst", // the login is gone: log in again
	codeNotLoggedIn:  "loginFirst",
	codeWrongCode:    "codeWrong",   // try the code again
	codeExpired:      "codeExpired", // ask for a new code
	codeRateLimited:  "rateLimited", // try again later
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(usage)
		return nil
	}
	if args[0] == "version" {
		if slices.Contains(args[1:], "--json") {
			// lang: the screen language GitFolio's texts are in, for an app to show its own in the same
			// release: the release number, also of a build from source ("0.6.0-dev" is 0.6.0); dev: one
			release := strings.TrimSuffix(version, "-dev")
			if version == "dev" {
				release = ""
			}
			res := map[string]any{"version": version, "release": release, "dev": sourceBuild(), "bundled": bundled != "",
				"lang": detectLang(os.Getenv)}
			// --latest also asks for the newest release, unless an app ships this copy (it updates it)
			if slices.Contains(args[1:], "--latest") && bundled == "" {
				if latest, err := latestRelease(); err == nil { // offline: left out
					res["latest"] = strings.TrimPrefix(latest, "v")
					if !sourceBuild() {
						res["upToDate"] = !newer(latest, version)
					}
				}
			}
			return printJSON(res)
		}
		fmt.Print(header())
		return nil
	}
	// --json first: a failure in the checks below comes as JSON too.
	jsonMode(args[1:])
	if args[0] != "hook" && !slices.Contains(commands, args[0]) { // before anything else runs for a typo
		return unknownCommand(args[0])
	}
	if args[0] == "git" { // whether git is there, before needing it
		return cmdGit(args[1:])
	}
	if args[0] == "init" && slices.Equal(withoutJSON(args[1:]), []string{"--policy"}) { // a fixed text: before git and the data folder
		dir, _ := dataPath() // named in the setup actions, not made
		return cmdInit(dir, args[1:])
	}
	if !gitWorks() && args[0] != "uninstall" && args[0] != "update" { // removing goes as far as it can; updating needs no git
		if args[0] != "init" || jsonOut || !interactive() || !installGit(detectLang(os.Getenv)) {
			return failure("noGit")
		}
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	startHistory(dir, args)
	if args[0] == "mcp" { // stdout is the protocol: no update question or notices before it
		return cmdMCP(dir, args[1:])
	}
	if !slices.Contains([]string{"hook", "update", "uninstall"}, args[0]) && checkUpdate(dir) {
		return nil // updated: the user runs the command again with the new binary
	}
	if !slices.Contains([]string{"hook", "init", "login", "logout", "email", "uninstall"}, args[0]) {
		checkEmails(dir, detectLang(os.Getenv))
		checkLocalMode(dir, detectLang(os.Getenv))
	}
	if collects(args) {
		c, err := newClient(dir) // only a login to the server this build talks to counts
		if err != nil {
			return err
		}
		if args[0] != "hook" {
			c.ensureLogin() // not logged in: a person at the terminal goes straight into logging in
		}
		if c.creds.Token == "" {
			if args[0] == "hook" {
				return nil // hooks do nothing before login
			}
			return failure("loginFirst")
		}
	}
	switch args[0] {
	case "hook":
		return cmdHook(dir, args[1:]) // takes the lock itself, only where it writes data
	case "init":
		return cmdInit(dir, args[1:]) // interactive; takes the lock only while writing
	case "login":
		return cmdLogin(dir, args[1:])
	case "logout":
		return cmdLogout(dir, args[1:])
	case "whoami":
		return cmdWhoami(dir, args[1:])
	case "history":
		return cmdHistory(dir, args[1:]) // only reads
	case "update":
		return cmdUpdate(dir, args[1:])
	case "uninstall":
		return cmdUninstall(dir, args[1:]) // takes the lock itself: --purge-local deletes it with the data
	case "stats":
		return cmdStats(dir, args[1:]) // outside the lock: it may wait on aline.team
	case "stack":
		return cmdStack(dir, args[1:]) // the same
	case "portfolio":
		return cmdPortfolio(dir, args[1:]) // the same; reads this computer's data under the lock
	case "email":
		if len(withoutJSON(args[1:])) == 0 {
			return cmdEmail(dir, args[1:]) // the list only reads, like whoami, and may wait on aline.team
		}
	}
	return withLock(dir, func() error {
		switch args[0] {
		case "add":
			return cmdAdd(dir, args[1:])
		case "remove":
			return cmdRemove(dir, args[1:])
		case "scan":
			return cmdScan(dir, args[1:])
		case "sync":
			return cmdSync(dir, args[1:])
		case "list":
			return cmdList(dir)
		case "status":
			return cmdStatus(dir, args[1:])
		case "export":
			return cmdExport(dir, args[1:])
		case "config":
			return cmdConfig(dir, args[1:])
		case "deps":
			return cmdDeps(dir, args[1:])
		case "schedule":
			return cmdSchedule(dir, args[1:])
		case "email":
			return cmdEmail(dir, args[1:])
		}
		return unknownCommand(args[0])
	})
}

// commands are the commands people type; "hook" is run by git hooks only and never suggested.
var commands = []string{"init", "login", "logout", "whoami", "add", "remove", "scan", "sync", "list", "status",
	"stats", "stack", "portfolio", "mcp", "export", "config", "deps", "schedule", "email", "history", "update", "uninstall", "git", "version", "help"}

// unknownCommand names the commands the user may have meant (similarCommands), or points to help.
func unknownCommand(s string) error {
	like := similarCommands(s)
	if len(like) == 0 {
		return failure("unknownCommand", s)
	}
	for i, c := range like {
		like[i] = "`gitfolio " + c + "`"
	}
	return failure("didYouMean", s, strings.Join(like, ", "))
}

// similarCommands are the commands s may have meant: version for -v and --version, the commands s
// starts (sta: status), then misspellings one or two edits away (stauts: status; one for short names).
func similarCommands(s string) []string {
	if s := strings.ToLower(strings.TrimLeft(s, "-")); s == "v" || s == "version" {
		return []string{"version"}
	}
	return similar(s, commands)
}

// similar are the words s may have meant: the ones s starts, then misspellings one or two edits away
// (one for short words). Flags are compared without their dashes (--dryrun: --dry-run).
func similar(s string, words []string) []string {
	s = strings.ToLower(strings.TrimLeft(s, "-"))
	bare := func(w string) string { return strings.TrimLeft(w, "-") }
	var prefix, close []string
	for _, w := range words {
		switch d := editDistance(s, bare(w)); {
		case len(s) >= 2 && strings.HasPrefix(bare(w), s):
			prefix = append(prefix, w)
		case d <= 1 || (d == 2 && len(bare(w)) > 4):
			close = append(close, w)
		}
	}
	slices.SortStableFunc(close, func(a, b string) int { return editDistance(s, bare(a)) - editDistance(s, bare(b)) }) // closest first
	return append(prefix, close...)
}

// wrongWord answers a word that cmd ("config autosync") does not take with the words the user may have
// meant from those it takes, else with its usage.
func wrongWord(cmd, got string, words []string, usage string) error {
	like := similar(got, words)
	if len(like) == 0 || slices.Contains(words, got) { // a right word in the wrong place
		return failure("usage", usage)
	}
	for i, w := range like {
		like[i] = "`gitfolio " + cmd + " " + w + "`"
	}
	return failure("didYouMeanWord", cmd, got, strings.Join(like, ", "))
}

// parseFlags is fs.Parse in GitFolio's words: an unknown flag is answered by wrongWord, nothing printed.
func parseFlags(fs *flag.FlagSet, args []string, usage string) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if err == nil {
		return nil
	}
	var flags []string
	fs.VisitAll(func(f *flag.Flag) { flags = append(flags, "--"+f.Name) })
	_, bad, _ := strings.Cut(err.Error(), "not defined: ") // "" for -h or a bad value: the usage
	// Shown as typed: "--dryrun", not the "-dryrun" the flag package reports.
	for _, a := range args {
		if name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "="); bad != "" && "-"+name == bad {
			bad = a
			break
		}
	}
	return wrongWord(fs.Name(), bad, flags, usage)
}

// editDistance counts the insertions, deletions, substitutions and swaps of neighbouring letters that
// turn a into b (optimal string alignment distance).
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}

// collects reports whether the command reads repositories. Nothing is collected before an aline.team
// login (DESIGN 6.1); commands that only show, clean up or configure keep working, so a logged-out
// user can still point to another server, remove repositories or turn dependency detection off.
func collects(args []string) bool {
	switch args[0] {
	case "add", "scan", "sync", "hook":
		return true
	case "deps":
		return len(args) > 1 && (args[1] == "on" || args[1] == "review" || args[1] == "scan")
	}
	return false
}

func pathArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}

// flagsFirst moves "-x" arguments before the others so "scan . --all" works like "scan --all .".
func flagsFirst(args []string) []string {
	var flags, rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}

func cmdAdd(dir string, args []string) error {
	jsonMode(args)
	top, err := topLevel(pathArg(withoutJSON(args)))
	if err != nil {
		return err
	}
	n, err := registerRepo(dir, newRepo(top), true)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(map[string]any{"name": filepath.Base(top), "path": top, "commits": n})
	}
	say(detectLang(os.Getenv), "registered", filepath.Base(top), n)
	return nil
}

// registerRepo registers repo (a git top-level), collects it and installs hooks, and returns how many
// commits it collected; the caller reports it. With ask, it first asks which package manager files may
// be read; init asks for all chosen repositories at once instead.
// Callers hold the data lock.
func registerRepo(dir string, repo Repo, ask bool) (int, error) {
	top := repo.Path
	repos, err := loadRepos(dir)
	if err != nil {
		return 0, err
	}
	for _, r := range repos {
		if r.Path == top {
			return 0, failure("alreadyRegistered", tildePath(top))
		}
	}
	repos = append(repos, repo)
	r := &repos[len(repos)-1]
	cfg, err := loadConfig(dir)
	if err != nil {
		return 0, err
	}
	if len(cfg.Emails) == 0 { // the first repository's git email becomes the primary work email
		if out, err := git(top, "config", "user.email"); err == nil && len(addEmails(&cfg, []string{out})) == 0 {
			if err := saveConfig(dir, cfg); err != nil {
				return 0, err
			}
		}
	}
	if ask && cfg.Deps && interactive() {
		reviewManifests(repos[len(repos)-1:])
	}
	n, err := scanRepo(dir, r, false)
	if err != nil {
		return 0, err
	}
	if err := saveRepos(dir, repos); err != nil {
		return 0, err
	}
	if err := installHooks(top); err != nil {
		warn(detectLang(os.Getenv), "hooksNotInstalled", err)
	}
	return n, nil
}

func cmdRemove(dir string, args []string) error {
	jsonMode(args)
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete this repository's collected commits")
	fs.Bool("json", false, "print the result as JSON")
	if err := parseFlags(fs, flagsFirst(args), "gitfolio remove [path] [--purge] [--json]"); err != nil {
		return err
	}
	p := pathArg(fs.Args())
	top, err := topLevel(p)
	if err != nil { // the folder may already be gone
		if top, err = filepath.Abs(p); err != nil {
			return err
		}
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(repos, func(r Repo) bool { return r.Path == top })
	if i < 0 {
		return failure("notRegistered", tildePath(top))
	}
	r := repos[i]
	hooksErr := ""
	if err := uninstallHooks(top); err != nil {
		warn(detectLang(os.Getenv), "hooksNotRestored", err)
		hooksErr = err.Error()
	}
	deleted, queued := 0, false
	if *purge {
		commits, err := readCommits(dir)
		if err != nil {
			return err
		}
		kept := slices.DeleteFunc(slices.Clone(commits), func(c Commit) bool { return c.Repo == r.ID })
		deleted = len(commits) - len(kept)
		if err := writeCommits(dir, kept); err != nil {
			return err
		}
		// aline.team keeps a repository another registered one still sends (a moved or second copy)
		shared := slices.ContainsFunc(repos, func(x Repo) bool {
			return x.ID != r.ID && x.Provider == r.Provider && x.Namespace == r.Namespace
		})
		if queued = r.Namespace != "" && !shared; queued {
			if err := queueDeletion(dir, deletion{r.Provider, r.Namespace}); err != nil {
				return err
			}
		} else if shared {
			notice(fmt.Sprintf(tr(detectLang(os.Getenv), "purgeShared"), r.Namespace))
		}
	}
	repos = slices.Delete(repos, i, i+1)
	if err := saveRepos(dir, repos); err != nil {
		return err
	}
	if len(repos) == 0 { // nothing left to sync: the daily sync goes too
		if cfg, err := loadConfig(dir); err == nil && cfg.Schedule != "" {
			if err := unschedule(dir); err != nil {
				return err
			}
			cfg.Schedule = ""
			if err := saveConfig(dir, cfg); err != nil {
				return err
			}
		}
	}
	if jsonOut { // what aline.team holds goes with the next sync (serverDeletionQueued)
		return printJSON(map[string]any{"name": r.Name, "path": r.Path, "hooksRestored": hooksErr == "", "hooksError": hooksErr,
			"commitsDeleted": deleted, "serverDeletionQueued": queued})
	}
	say(detectLang(os.Getenv), "removed", r.Name)
	if queued {
		say(detectLang(os.Getenv), "purgeQueued")
	}
	return nil
}

// cmdUninstall undoes what GitFolio set up on this computer, before it is removed (an app's uninstaller
// runs it): every repository is unregistered and its hooks give way to the ones before, the daily sync
// goes, and this device's login is revoked when aline.team answers in time. --purge-local also deletes
// GitFolio's data here. Nothing on aline.team is deleted, nothing is asked, and running it again does
// no harm.
func cmdUninstall(dir string, args []string) error {
	jsonMode(args)
	purge := false
	for _, a := range args {
		switch a {
		case "--purge-local":
			purge = true
		case "--json":
		default:
			return failure("usage", "gitfolio uninstall [--purge-local] [--json]")
		}
	}
	lang := detectLang(os.Getenv)
	type repo struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Error string `json:"error,omitempty"` // its hooks could not be put back
	}
	res := struct {
		Repositories []repo `json:"repositories"`
		Revoked      bool   `json:"revoked"` // aline.team revoked this device's login
		DataDeleted  bool   `json:"dataDeleted"`
	}{Repositories: []repo{}}
	err := withLock(dir, func() error {
		repos, err := loadRepos(dir)
		if err != nil {
			return err
		}
		for _, r := range repos {
			e := ""
			if _, err := os.Stat(r.Path); err == nil { // a folder that is gone took its hooks with it
				if err := uninstallHooks(r.Path); err != nil {
					warn(lang, "hooksNotRestored", fmt.Errorf("%s: %w", r.Name, err))
					e = err.Error()
				}
			}
			res.Repositories = append(res.Repositories, repo{r.Name, r.Path, e})
		}
		if err := saveRepos(dir, []Repo{}); err != nil {
			return err
		}
		if cfg, err := loadConfig(dir); err != nil {
			return err
		} else if cfg.Schedule != "" {
			if err := unschedule(dir); err != nil {
				return err
			}
			cfg.Schedule = ""
			if err := saveConfig(dir, cfg); err != nil {
				return err
			}
		}
		apiTimeout = 5 * time.Second // an uninstaller does not wait long on the network
		c, err := newClient(dir)
		if err != nil {
			return err
		}
		if c.loggedIn() { // send, not call: an expired login is not renewed only to be revoked
			if err := c.send("POST", "/cli/logout", nil, nil, true); err != nil {
				warn(lang, "logoutServerFailed", err)
			} else {
				res.Revoked = true
			}
		}
		return saveCredentials(dir, Credentials{})
	})
	if err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		res.DataDeleted = true
	}
	if jsonOut {
		return printJSON(res)
	}
	say(lang, "uninstalled", len(res.Repositories))
	if purge {
		say(lang, "uninstalledData", tildePath(dir))
	} else {
		notice(fmt.Sprintf(tr(lang, "uninstalledKept"), tildePath(dir)))
	}
	return nil
}

func cmdScan(dir string, args []string) error {
	jsonMode(args)
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	all := fs.Bool("all", false, "scan every registered repository")
	rebuild := fs.Bool("rebuild", false, "drop stored commits and collect again")
	fs.Bool("json", false, "print the result as JSON")
	if err := parseFlags(fs, flagsFirst(args), "gitfolio scan [path] [--all] [--rebuild] [--json]"); err != nil {
		return err
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	var top string
	if !*all {
		if top, err = topLevel(pathArg(fs.Args())); err != nil {
			return err
		}
	}
	lang := detectLang(os.Getenv)
	var failed, matched bool
	type result struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Commits int    `json:"commits"`          // collected now
		Pending int    `json:"pendingManifests"` // package manager files not decided on yet (deps on)
		Error   string `json:"error,omitempty"`
	}
	results := []result{}
	var rs []*Repo
	for i := range repos {
		if *all || repos[i].Path == top {
			rs = append(rs, &repos[i])
		}
	}
	matched = len(rs) > 0
	if *all {
		progress(lang, "progressScan", 0, len(rs))
	}
	err = scanRepos(dir, rs, *rebuild, func(i, n int, err error) {
		r := rs[i]
		if *all {
			defer progress(lang, "progressScan", i+1, len(rs))
		}
		if err != nil {
			warn(lang, "repoFailed", r.Name, err)
			failed = true
			results = append(results, result{Name: r.Name, Path: r.Path, Error: err.Error()})
			return
		}
		results = append(results, result{Name: r.Name, Path: r.Path, Commits: n})
		msg := fmt.Sprintf(tr(lang, "scanned"), r.Name, n)
		if cfg.Deps && cfg.DepsAuto {
			if _, _, err := refreshDeps(dir, r); err != nil {
				warn(lang, "repoFailed", r.Name, err)
				failed = true
				results[len(results)-1].Error = err.Error()
			}
		}
		if cfg.Deps {
			if n := pendingManifests(*r); n > 0 {
				msg += fmt.Sprintf(tr(lang, "depsPending"), n, r.Path)
				results[len(results)-1].Pending = n
			}
		}
		show(out, msg)
	})
	progressDone()
	if err != nil {
		return err
	}
	if !*all && !matched {
		return failure("notRegisteredAdd", tildePath(top))
	}
	if err := saveRepos(dir, repos); err != nil {
		return err
	}
	if jsonOut { // a repository that failed says so in its error; the others were collected
		return printJSON(map[string]any{"repositories": results})
	}
	if failed {
		return failure("scanFailed")
	}
	return nil
}

func cmdList(dir string) error {
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	commits, err := readCommits(dir)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, c := range commits {
		count[c.Repo]++
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCOMMITS\tHOOKS\tDEPS\tLAST SCAN\tPATH")
	hooks, pending := make([]string, len(repos)), make([]int, len(repos))
	sideBySide(len(repos), func(i int) {
		if hooks[i] = hookStatus(repos[i].Path); cfg.Deps {
			pending[i] = pendingManifests(repos[i])
		}
	})
	for i, r := range repos {
		deps := "off"
		if cfg.Deps {
			allowed := 0
			for _, ok := range r.Manifests {
				if ok {
					allowed++
				}
			}
			deps = fmt.Sprintf("%d files", allowed)
			if n := pending[i]; n > 0 {
				deps += fmt.Sprintf(" (%d to review)", n)
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", r.Name, count[r.ID], hooks[i], deps, r.LastScan, r.Path)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	st, err := loadSync(dir)
	if err != nil {
		return err
	}
	waitingNotice(detectLang(os.Getenv), st.Waiting) // also after pushes, which send in the background
	return nil
}

type exportData struct {
	Commits      []Commit     `json:"commits"`
	Dependencies []Dependency `json:"dependencies"`
	Snapshots    []snapshot   `json:"snapshots"` // what aline.team gets of each repository (snapshot.go)
}

// buildExport assembles what leaves the computer: repository names and namespaces instead of local IDs,
// only the dependencies of modules the user's own commits touched, and the snapshots. Module IDs stay local.
func buildExport(dir string) (exportData, error) {
	cfg, err := loadConfig(dir)
	if err != nil {
		return exportData{}, err
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return exportData{}, err
	}
	commits, err := readCommits(dir)
	if err != nil {
		return exportData{}, err
	}
	deps, err := loadDeps(dir)
	if err != nil {
		return exportData{}, err
	}
	byID := map[string]Repo{}
	for _, r := range repos {
		byID[r.ID] = r
	}
	out := exportData{make([]Commit, 0, len(commits)), make([]Dependency, 0, len(deps)), snapshots(repos, commits, primaryEmail(cfg))}
	touched := map[string]bool{} // repo ID + "/" + module ID
	for _, c := range commits {
		for i := range c.Files { // commits is this function's own copy: module IDs are cleared in place
			if c.Files[i].Module != "" {
				touched[c.Repo+"/"+c.Files[i].Module] = true
			}
			c.Files[i].Module = ""
		}
		r, ok := byID[c.Repo]
		if !ok { // left behind by remove: of no registered repository, so nothing to send
			continue
		}
		c.Repo, c.Provider, c.Namespace = r.Name, r.Provider, r.Namespace
		out.Commits = append(out.Commits, c)
	}
	seen := map[string]bool{}
	for _, d := range deps {
		key := d.Repo + "/" + d.Ecosystem + "/" + d.Name
		if !touched[d.Repo+"/"+d.Module] || seen[key] {
			continue
		}
		seen[key] = true
		r := byID[d.Repo]
		d.Repo, d.Provider, d.Namespace, d.Module = r.Name, r.Provider, r.Namespace, ""
		out.Dependencies = append(out.Dependencies, d)
	}
	return out, nil
}

func cmdExport(dir string, args []string) error {
	if len(withoutJSON(args)) > 0 { // JSON either way; --json makes a failure JSON too (run)
		return failure("usage", "gitfolio export [--json]")
	}
	out, err := buildExport(dir)
	if err != nil {
		return err
	}
	return printJSON(out)
}

// printJSON prints v indented, keeping "<a@b.com>" readable instead of <…>.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// cmdConfig shows the settings as JSON, or changes one; with --json a change is followed by the settings.
func cmdConfig(dir string, args []string) error {
	jsonMode(args)
	args = withoutJSON(args)
	if err := changeConfig(dir, args); err != nil || !jsonOut || len(args) == 0 || args[0] == "ai" { // config ai prints its own
		return err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	return printJSON(cfg)
}

func changeConfig(dir string, args []string) error {
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return printJSON(cfg)
	}
	if args[0] == "autosync" && len(args) == 2 && (args[1] == "on" || args[1] == "off") {
		cfg.AutoSyncOff, cfg.LocalModeKnown = args[1] == "off", true
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		lang := detectLang(os.Getenv)
		say(lang, "autosync", tr(lang, map[bool]string{true: "syncAuto", false: "syncLocal"}[!cfg.AutoSyncOff]))
		if cfg.AutoSyncOff && cfg.Schedule != "" { // local mode sends nothing by itself: no daily sync either
			return setSchedule(dir, lang, "")
		}
		return nil
	}
	if args[0] == "git-history" && len(args) == 2 && (args[1] == "on" || args[1] == "off") {
		cfg.GitHistoryOff = args[1] == "off"
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		lang := detectLang(os.Getenv)
		if cfg.GitHistoryOff {
			gitTrace = "" // nothing more is written by this run
			if err := os.Remove(filepath.Join(dir, historyFile)); err != nil && !os.IsNotExist(err) {
				return err
			}
			say(lang, "historyTurnedOff")
			return nil
		}
		say(lang, "historyTurnedOn")
		return nil
	}
	// Development only: not in the help, and release builds refuse it (apiBase ignores it there too).
	if args[0] == "api-url" && sourceBuild() { // aline.team server, e.g. the development server; "default" = production
		cfg.APIURL = ""
		if len(args) > 1 && args[1] != "default" {
			if err := checkAPIURL(args[1]); err != nil {
				return err
			}
			cfg.APIURL = strings.TrimRight(args[1], "/")
		}
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		base, err := apiBase(cfg.APIURL)
		if err != nil {
			return err
		}
		say(detectLang(os.Getenv), "apiURL", base)
		return nil
	}
	if args[0] == "ai" {
		return cmdConfigAI(dir, args[1:])
	}
	if len(args) < 3 || args[0] != "mask" || (args[1] != "add" && args[1] != "rm") {
		const usage = "gitfolio config mask add|rm <word>... | config autosync on|off | config git-history on|off | config ai ..."
		switch {
		case !slices.Contains([]string{"mask", "autosync", "git-history", "ai"}, args[0]):
			return wrongWord("config", args[0], []string{"mask", "autosync", "git-history", "ai"}, usage)
		case len(args) > 1 && args[0] == "mask":
			return wrongWord("config mask", args[1], []string{"add", "rm"}, usage)
		case len(args) > 1:
			return wrongWord("config "+args[0], args[1], []string{"on", "off"}, usage)
		}
		return failure("usage", usage)
	}
	for _, w := range args[2:] {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		i := slices.IndexFunc(cfg.Mask, func(x string) bool { return strings.EqualFold(x, w) })
		switch {
		case args[1] == "add" && i < 0:
			cfg.Mask = append(cfg.Mask, w)
		case args[1] == "rm" && i >= 0:
			cfg.Mask = slices.Delete(cfg.Mask, i, i+1)
		}
	}
	if err := saveConfig(dir, cfg); err != nil {
		return err
	}
	if args[1] == "rm" {
		say(detectLang(os.Getenv), "maskRemoved")
		return nil
	}
	return remask(dir, newMasker(cfg.Mask))
}

// remask applies m to the stored commit messages, so a newly blocked word disappears from past data too.
func remask(dir string, m masker) error {
	n, err := maskStored(dir, m)
	if err != nil {
		return err
	}
	say(detectLang(os.Getenv), "maskApplied", n)
	return nil
}

// maskStored applies m to the stored commit messages and says how many there are. Callers hold the data lock.
func maskStored(dir string, m masker) (int, error) {
	commits, err := readCommits(dir)
	if err != nil {
		return 0, err
	}
	for i := range commits {
		m.commit(&commits[i])
	}
	return len(commits), writeCommits(dir, commits)
}
