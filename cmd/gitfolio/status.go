package main

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// languages maps a file extension to the language its changed lines count for: the languages aline.team
// counts as a tech stack, with the same extensions, so the shares match what it shows. Data, prose and
// build files (JSON, YAML, Markdown, Dockerfile, ...) count for none.
var languages = map[string]string{
	"c": "C", "h": "C", "cpp": "C++", "c++": "C++", "cc": "C++", "cxx": "C++", "hpp": "C++", "hxx": "C++",
	"cs": "C#", "java": "Java", "jsp": "Java", "properties": "Java Properties",
	"js": "JavaScript", "jsx": "JavaScript", "mjs": "JavaScript", "cjs": "JavaScript",
	"ts": "TypeScript", "tsx": "TypeScript", "mts": "TypeScript", "cts": "TypeScript",
	"py": "Python", "pyi": "Python", "pyw": "Python", "go": "Go", "rs": "Rust",
	"rb": "Ruby", "rake": "Ruby", "gemspec": "Ruby", "php": "PHP", "php3": "PHP", "php4": "PHP", "php5": "PHP",
	"swift": "Swift", "kt": "Kotlin", "kts": "Kotlin", "scala": "Scala", "sc": "Scala",
	"sh": "Shell", "bash": "Shell", "zsh": "Shell", "fish": "Shell",
	"html": "HTML", "htm": "HTML", "xhtml": "HTML", "css": "CSS", "scss": "CSS", "sass": "CSS", "less": "CSS",
	"sql": "SQL", "dart": "Dart", "r": "R", "lua": "Lua", "perl": "Perl", "pl": "Perl", "asm": "Assembly", "s": "Assembly",
	"ex": "Elixir", "exs": "Elixir", "m": "Objective-C", "mm": "Objective-C", "groovy": "Groovy", "gvy": "Groovy",
	"clj": "Clojure", "cljs": "Clojure", "cljc": "Clojure", "hs": "Haskell", "lhs": "Haskell",
	"erl": "Erlang", "hrl": "Erlang", "fs": "F#", "fsi": "F#", "fsx": "F#", "jl": "Julia", "sol": "Solidity",
	"ps1": "PowerShell", "psm1": "PowerShell", "psd1": "PowerShell", "v": "Verilog", "vh": "Verilog",
	"sv": "Verilog", "svh": "Verilog", "vhd": "VHDL", "vhdl": "VHDL", "zig": "Zig", "gd": "GDScript",
}

// languageOf is the language of a file path in the repository, "" for none. Only the extension counts,
// in any case; a name without one, or a hidden file like ".bashrc", has none.
func languageOf(file string) string {
	base := path.Base(file)
	ext := path.Ext(base)
	if ext == base || len(ext) < 2 {
		return ""
	}
	return languages[strings.ToLower(ext[1:])]
}

// repoStatus is what GitFolio holds for one repository: the user's commits collected on this computer,
// how many of them aline.team has, and what they show of the stack.
type repoStatus struct {
	commits, sent, changes, created, add, del, ai int
	first, last                                   time.Time
	lines                                         map[string]int // changed lines per language
	agents                                        map[string]int // commits per AI agent
	deps                                          map[string][]string
}

func statusOf(r Repo, commits []Commit, deps []Dependency, st syncState) repoStatus {
	s := repoStatus{lines: map[string]int{}, agents: map[string]int{}, deps: map[string][]string{}}
	for _, c := range commits {
		if c.Repo != r.ID {
			continue
		}
		s.commits++
		if r.Namespace != "" && st.Commits[r.Provider+"/"+r.Namespace+"/"+c.Hash] != "" {
			s.sent++
		}
		if c.CreationType != "" && c.CreationType != "HUMAN" {
			s.ai++
		}
		for _, a := range c.AIAgents {
			s.agents[a]++
		}
		if t, err := time.Parse(time.RFC3339, c.Date); err == nil {
			if t.After(s.last) {
				s.last = t
			}
			if s.first.IsZero() || t.Before(s.first) {
				s.first = t
			}
		}
		for _, f := range c.Files {
			s.changes++
			s.add += f.Add
			s.del += f.Del
			if f.Created {
				s.created++
			}
			if l := languageOf(f.Name); l != "" && f.Add+f.Del > 0 { // binary files change no lines
				s.lines[l] += f.Add + f.Del
			}
		}
	}
	for _, d := range deps {
		if d.Repo == r.ID {
			s.deps[d.Ecosystem] = append(s.deps[d.Ecosystem], strings.TrimSpace(d.Name+" "+d.Version))
		}
	}
	return s
}

// languageShares lists the languages by their share of changed lines, the largest first, at most n (0: all).
func (s repoStatus) languageShares(n int) string {
	total := 0
	for _, v := range s.lines {
		total += v
	}
	if total == 0 {
		return "-"
	}
	names := slices.SortedFunc(maps.Keys(s.lines), func(a, b string) int {
		return cmp.Or(cmp.Compare(s.lines[b], s.lines[a]), cmp.Compare(a, b))
	})
	if n > 0 && len(names) > n {
		names = names[:n]
	}
	parts := make([]string, len(names))
	for i, l := range names {
		pct := fmt.Sprintf("%d%%", (s.lines[l]*100+total/2)/total)
		if pct == "0%" {
			pct = "<1%"
		}
		parts[i] = l + " " + pct
	}
	return strings.Join(parts, ", ")
}

// depCounts is "npm 64, go 3", or "off" when dependency detection is off.
func (s repoStatus) depCounts(on bool) string {
	if !on {
		return "off"
	}
	if len(s.deps) == 0 {
		return "-"
	}
	var parts []string
	for _, eco := range slices.Sorted(maps.Keys(s.deps)) {
		parts = append(parts, fmt.Sprintf("%s %d", eco, len(s.deps[eco])))
	}
	return strings.Join(parts, ", ")
}

func (s repoStatus) aiShare() string {
	if s.commits == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (s.ai*100+s.commits/2)/s.commits)
}

func (s repoStatus) lastCommit() string {
	if s.last.IsZero() {
		return "-"
	}
	return s.last.Local().Format("2006-01-02")
}

// sentCount is how many of the commits aline.team has: "-" without a git service remote (never sent),
// "waiting" while aline.team takes the repository only after an email is verified.
func (s repoStatus) sentCount(r Repo, st syncState) string {
	switch {
	case r.Namespace == "":
		return "-"
	case st.Waiting[r.Provider+"/"+r.Namespace] != "":
		return "waiting"
	}
	return fmt.Sprint(s.sent)
}

// cmdStatus shows what GitFolio has collected, from this computer's data only (no request to aline.team):
// per repository with no argument, in full for one repository (its name or path).
func cmdStatus(dir string, args []string) error {
	jsonMode(args)
	if args = withoutJSON(args); len(args) > 1 {
		return failure("usage", "gitfolio status [name|path] [--json]")
	}
	lang := detectLang(os.Getenv)
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	commits, err := readCommits(dir)
	if err != nil {
		return err
	}
	deps, err := loadDeps(dir)
	if err != nil {
		return err
	}
	st, err := loadSync(dir)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if jsonOut && len(args) == 0 {
		return statusJSON(dir, repos, commits, st, cfg)
	}
	if len(args) > 0 {
		r, ok := findRepo(repos, args[0])
		if !ok {
			return failure("notRegistered", args[0])
		}
		s := statusOf(r, commits, deps, st)
		if jsonOut {
			return printJSON(repoJSON(r, s, st, cfg.Deps))
		}
		agents := "-"
		if len(s.agents) > 0 {
			var parts []string
			for _, a := range slices.Sorted(maps.Keys(s.agents)) {
				parts = append(parts, fmt.Sprintf("%s %d", a, s.agents[a]))
			}
			agents = strings.Join(parts, ", ")
		}
		where := cmp.Or(strings.TrimSpace(r.Provider+" "+r.Namespace), "-")
		sayKV(lang, "statusRepo", r.Name, where, s.commits, s.sentCount(r, st), s.changes, gitLines(fmt.Sprintf("+%d -%d", s.add, s.del)), s.created,
			s.aiShare(), agents, s.languageShares(0), s.depCounts(cfg.Deps), s.lastCommit())
		if cfg.Deps {
			for _, eco := range slices.Sorted(maps.Keys(s.deps)) {
				names := slices.Sorted(slices.Values(s.deps[eco]))
				notice(fmt.Sprintf("%s (%d): %s\n", eco, len(names), strings.Join(names, ", ")))
			}
		}
		if key := r.Provider + "/" + r.Namespace; st.Waiting[key] != "" {
			c, err := newClient(dir)
			if err != nil {
				return err
			}
			waitingNotice(lang, c, map[string]string{key: st.Waiting[key]})
		}
		return nil
	}
	if len(repos) == 0 {
		say(lang, "statusNone")
		return nil
	}
	// A result paragraph like the others (DESIGN 7.1): the title after the mark, the table under it.
	// The table is aligned first and its header dimmed after, so color codes do not shift the columns.
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCOMMITS\tSENT\tFILE CHANGES\tLINES\tAI\tLANGUAGES\tDEPENDENCIES\tLAST COMMIT")
	for _, r := range repos {
		s := statusOf(r, commits, deps, st)
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t+%d -%d\t%s\t%s\t%s\t%s\n", r.Name, s.commits, s.sentCount(r, st), s.changes,
			s.add, s.del, s.aiShare(), s.languageShares(3), s.depCounts(cfg.Deps), s.lastCommit())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	header, rows, _ := strings.Cut(b.String(), "\n")
	title := strings.TrimRight(fmt.Sprintf(tr(lang, "statusTitle"), len(repos)), "\n")
	show(out, title+"\n"+dim(strings.TrimRight(header, " "))+"\n"+gitLines(rows))
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	waitingNotice(lang, c, st.Waiting)
	notice(tr(lang, "statusMore"))
	return nil
}

// statusJSON is gitfolio status --json: what GitFolio holds on this computer, for a program, without
// asking aline.team. setUp is true once logged in with a repository registered (what init does).
func statusJSON(dir string, repos []Repo, commits []Commit, st syncState, cfg Config) error {
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	type repo struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		Namespace  string `json:"namespace"` // owner/name on the git service; "" without one: never sent
		Commits    int    `json:"commits"`
		Sent       int    `json:"sent"`    // how many of them aline.team has
		Waiting    bool   `json:"waiting"` // aline.team takes them once an email is verified (gitfolio email)
		Hooks      string `json:"hooks"`   // on, off, or manual: a hooks folder GitFolio does not write to
		LastCommit string `json:"lastCommit,omitempty"`
	}
	list, unsent, hooks, waiting := []repo{}, 0, make([]string, len(repos)), map[string]string{}
	sideBySide(len(repos), func(i int) { hooks[i] = hookStatus(repos[i].Path) })
	for i, r := range repos {
		s := statusOf(r, commits, nil, st)
		last := ""
		if !s.last.IsZero() {
			last = s.lastCommit()
		}
		key := r.Provider + "/" + r.Namespace
		list = append(list, repo{r.Name, r.Path, r.Namespace, s.commits, s.sent, st.Waiting[key] != "", hooks[i], last})
		if st.Waiting[key] != "" { // a repository removed since the last sync no longer waits
			waiting[key] = st.Waiting[key]
		}
		if r.Namespace != "" {
			unsent += s.commits - s.sent
		}
	}
	var account any // null: not logged in
	if c.loggedIn() {
		account = c.creds.Email
	}
	mode := "automatic"
	if cfg.AutoSyncOff {
		mode = "local" // commits are never sent: unsent only counts in automatic mode
	}
	return printJSON(map[string]any{"account": account, "setUp": c.loggedIn() && len(repos) > 0, "syncMode": mode,
		"lastSync": st.LastSync, "lastSyncError": st.LastError, "unsent": unsent, "repositories": list, "waiting": c.waitingJSON(waiting)})
}

// repoJSON is status <name> --json: what the text shows of one repository. dependencies is null while
// dependency detection is off; languages are changed lines per language, agents commits per AI agent.
func repoJSON(r Repo, s repoStatus, st syncState, depsOn bool) map[string]any {
	day := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format(time.DateOnly)
	}
	var deps map[string][]string
	if depsOn {
		deps = s.deps
	}
	return map[string]any{"name": r.Name, "path": r.Path, "service": r.Provider, "namespace": r.Namespace,
		"commits": s.commits, "sent": s.sent, "waiting": st.Waiting[r.Provider+"/"+r.Namespace] != "", "fileChanges": s.changes,
		"linesAdded": s.add, "linesDeleted": s.del, "filesCreated": s.created, "aiCommits": s.ai, "agents": s.agents,
		"languages": s.lines, "dependencies": deps, "lastCommit": day(s.last), "hooks": hookStatus(r.Path)}
}

// lineCounts is a "+added -deleted" pair of changed-line counts.
var lineCounts = regexp.MustCompile(`\+(\d+) -(\d+)`)

// gitLines colors changed-line counts the way git does: added green, deleted red. It works on text
// already aligned, so the color codes never shift a column, and ends each color with the default
// color only (39), so a bold value stays bold after it.
func gitLines(s string) string {
	if !useColor {
		return s
	}
	return lineCounts.ReplaceAllString(s, "\x1b[32m+${1}\x1b[39m \x1b[31m-${2}\x1b[39m")
}

// findRepo finds a registered repository by its name or by a path inside it.
func findRepo(repos []Repo, arg string) (Repo, bool) {
	if i := slices.IndexFunc(repos, func(r Repo) bool { return r.Name == arg }); i >= 0 {
		return repos[i], true
	}
	top, err := topLevel(arg)
	if err != nil {
		if top, err = filepath.Abs(arg); err != nil {
			return Repo{}, false
		}
	}
	i := slices.IndexFunc(repos, func(r Repo) bool { return r.Path == top })
	if i < 0 {
		return Repo{}, false
	}
	return repos[i], true
}
