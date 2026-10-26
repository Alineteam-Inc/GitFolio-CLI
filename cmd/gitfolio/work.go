package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The kinds of work a changed file is. Judged on this computer from its path; only the kind ever
// leaves it, never the path. Kept apart from packageFiles (snapshot.go) on purpose: that list is
// aline.team's rule for code files and follows the server, while these sort the user's work for AI
// tools, where a file a language reads stays source (setup.py, Package.swift, mix.exs).
var (
	lockFiles = set("package-lock.json yarn.lock pnpm-lock.yaml bun.lockb go.sum cargo.lock gemfile.lock composer.lock " +
		"poetry.lock pipfile.lock gradle.lockfile packages.lock.json")
	buildFiles = set("pom.xml build.gradle build.gradle.kts settings.gradle settings.gradle.kts package.json go.mod cargo.toml " +
		"pyproject.toml requirements.txt gemfile composer.json makefile cmakelists.txt pubspec.yaml")
	deployDirs = set(".github .circleci .gitlab k8s kubernetes helm charts terraform ansible")
	testDirs   = set("test tests __tests__ spec specs testdata e2e androidtest")
	docExts    = set("md markdown rst adoc txt")
	configExts = set("yml yaml json toml ini properties env conf cfg xml plist")
	// testSuffixed are the languages whose tests are named FooTest or FooTests.
	testSuffixed = set("java kt kts scala groovy cs swift php")
)

// workKind is source, test, docs, build, ci_deploy, database, config or other; "" for a lock file,
// which changes by the thousand without anyone writing it.
func workKind(p string) string {
	name := strings.TrimSuffix(path.Base(p), path.Ext(p)) // as written: FooTest is a test, Latest is not
	p = strings.ToLower(p)
	base := path.Base(p)
	ext := strings.TrimPrefix(path.Ext(base), ".")
	dirs := strings.Split(p, "/")
	dirs = dirs[:len(dirs)-1]
	in := func(names map[string]bool) bool {
		return slices.ContainsFunc(dirs, func(d string) bool { return names[d] })
	}
	switch {
	case lockFiles[base]:
		return ""
	case in(testDirs) || strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.Contains(base, "_spec.") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		testSuffixed[ext] && (strings.HasSuffix(name, "Test") || strings.HasSuffix(name, "Tests")):
		return "test"
	case strings.HasPrefix(base, "dockerfile") || strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose.") ||
		base == "jenkinsfile" || base == ".gitlab-ci.yml" || base == "procfile" || ext == "tf" || ext == "tfvars" || in(deployDirs):
		return "ci_deploy"
	case buildFiles[base] || strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".sln"):
		return "build"
	case ext == "sql" || slices.Contains(dirs, "migrations") || slices.Contains(dirs, "migration"):
		return "database"
	case docExts[ext] || slices.Contains(dirs, "docs") || slices.Contains(dirs, "doc"):
		return "docs"
	case configExts[ext]:
		return "config"
	case languageOf(base) != "":
		return "source"
	}
	return "other"
}

// commitKinds are the commit types (commitType) as plain words.
var commitKinds = map[string]string{"ADD": "feature", "FIX": "fix", "HOTFIX": "hotfix", "REFACTOR": "refactor", "PERF": "performance",
	"TEST": "test", "DOCUMENTATION": "docs", "CHORE": "chore", "CI": "ci", "REVERT": "revert", "WIP": "wip", "ENV": "environment",
	"SYSTEM": "merge", "UNCATEGORIZED": "other"}

// period is the part of the history asked for: from its first day to the end of its last one.
// Each end is a year, a month or a day ("2025", "2025-06", "2025-06-15"); empty is open.
type period struct{ from, to time.Time }

func parsePeriod(from, to string) (period, error) {
	var p period
	parse := func(s string, end bool) (time.Time, error) {
		for _, f := range []struct {
			layout string
			years  int
			months int
			days   int
		}{{"2006", 1, 0, 0}, {"2006-01", 0, 1, 0}, {time.DateOnly, 0, 0, 1}} {
			if t, err := time.ParseInLocation(f.layout, s, time.Local); err == nil {
				if end {
					t = t.AddDate(f.years, f.months, f.days)
				}
				return t, nil
			}
		}
		return time.Time{}, errors.New("not a year, month or day (2025, 2025-06 or 2025-06-15): " + s)
	}
	var err error
	if from != "" {
		if p.from, err = parse(from, false); err != nil {
			return p, err
		}
	}
	if to != "" {
		if p.to, err = parse(to, true); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (p period) has(t time.Time) bool {
	return !t.Before(p.from) && (p.to.IsZero() || t.Before(p.to))
}

// shares turns counts into percentages with one decimal; what rounds to zero is left out.
func shares(counts map[string]int) map[string]float64 {
	total := 0
	for _, n := range counts {
		total += n
	}
	out := map[string]float64{}
	for k, n := range counts {
		if pct := math.Round(float64(n)*1000/float64(max(total, 1))) / 10; pct > 0 {
			out[k] = pct
		}
	}
	return out
}

type workSummary struct {
	Repository   string             `json:"repository,omitempty"`
	Service      string             `json:"service,omitempty"`
	FirstCommit  string             `json:"firstCommit,omitempty"`
	LastCommit   string             `json:"lastCommit,omitempty"`
	ActiveMonths int                `json:"activeMonths"`
	Commits      int                `json:"commits"`
	CommitTypes  map[string]float64 `json:"commitTypesPercent"`
	WorkKinds    map[string]float64 `json:"workKindsPercentOfChangedLines"`
	Languages    map[string]float64 `json:"languagesPercentOfChangedLines"`
	LinesAdded   int                `json:"linesAdded"`
	LinesDeleted int                `json:"linesDeleted"`
	FilesCreated int                `json:"filesCreated"`
	WithAI       float64            `json:"withAIPercent"`
	AIAgents     map[string]int     `json:"commitsByAIAgent,omitempty"`
	Branches     []string           `json:"branches,omitempty"` // only where config ai shows them
}

// summarize counts the commits of one repository (or all, for the total) in the period.
func summarize(commits []Commit, p period, branches bool) workSummary {
	var s workSummary
	types, kinds, langs, months := map[string]int{}, map[string]int{}, map[string]int{}, map[string]bool{}
	var first, last time.Time
	ai, agents, seen := 0, map[string]int{}, map[string]bool{}
	for _, c := range commits {
		t, err := time.Parse(time.RFC3339, c.Date)
		if err != nil || !p.has(t.Local()) {
			continue
		}
		s.Commits++
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if t.After(last) {
			last = t
		}
		months[t.Local().Format("2006-01")] = true
		types[cmp.Or(commitKinds[commitType(c.Message)], "other")]++
		if c.CreationType != "" && c.CreationType != creationHuman {
			ai++
		}
		for _, a := range c.AIAgents {
			agents[a]++
		}
		if branches && c.Branch != "" && !seen[c.Branch] {
			seen[c.Branch] = true
			s.Branches = append(s.Branches, c.Branch)
		}
		for _, f := range c.Files {
			s.LinesAdded += f.Add
			s.LinesDeleted += f.Del
			if f.Created {
				s.FilesCreated++
			}
			if k := workKind(f.Name); k != "" {
				kinds[k] += f.Add + f.Del
			}
			if l := languageOf(f.Name); l != "" {
				langs[l] += f.Add + f.Del
			}
		}
	}
	if s.Commits == 0 {
		return s
	}
	s.FirstCommit, s.LastCommit = first.Local().Format(time.DateOnly), last.Local().Format(time.DateOnly)
	s.ActiveMonths = len(months)
	s.CommitTypes, s.WorkKinds, s.Languages = shares(types), shares(kinds), shares(langs)
	s.WithAI = math.Round(float64(ai)*1000/float64(s.Commits)) / 10
	if len(agents) > 0 {
		s.AIAgents = agents
	}
	slices.Sort(s.Branches)
	return s
}

// aiCommits are the commits of the repositories AI tools may see, by the name they see (one, if
// asked for), with the branch names shown where config ai lets them.
func aiCommits(dir string, args map[string]any) (repos []Repo, byRepo map[string][]Commit, p period, err error) {
	lang := detectLang(os.Getenv)
	var all []Repo
	var commits []Commit
	if err = withLock(dir, func() error {
		var err error
		if all, err = loadRepos(dir); err != nil {
			return err
		}
		commits, err = readCommits(dir)
		return err
	}); err != nil {
		return
	}
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)
	if p, err = parsePeriod(from, to); err != nil {
		return
	}
	repos = aiView(lang, all)
	if want, _ := args["repository"].(string); want != "" {
		i, ok := aiRepo(lang, all, want)
		if !ok || i < 0 || all[i].AI.Hidden {
			return nil, nil, p, failure("notRegistered", want)
		}
		repos = slices.DeleteFunc(repos, func(r Repo) bool { return r.ID != all[i].ID })
	}
	byRepo = map[string][]Commit{}
	for _, c := range commits {
		byRepo[c.Repo] = append(byRepo[c.Repo], c)
	}
	return repos, byRepo, p, nil
}

func mcpWork(dir string, args map[string]any) (any, error) {
	repos, byRepo, p, err := aiCommits(dir, args)
	if err != nil {
		return nil, err
	}
	var list []workSummary
	var everything []Commit
	for _, r := range repos {
		s := summarize(byRepo[r.ID], p, r.AI.Branches)
		if s.Commits == 0 {
			continue
		}
		s.Repository, s.Service = r.Name, providerNames[r.Provider]
		list = append(list, s)
		everything = append(everything, byRepo[r.ID]...)
	}
	slices.SortStableFunc(list, func(a, b workSummary) int { return strings.Compare(b.LastCommit, a.LastCommit) })
	total := summarize(everything, p, false)
	return map[string]any{"repositories": list, "total": total}, nil
}

type month struct {
	Month        string             `json:"month"`
	Commits      int                `json:"commits"`
	Repositories []string           `json:"repositories"`
	Languages    map[string]float64 `json:"languagesPercentOfChangedLines,omitempty"`
	CommitTypes  map[string]float64 `json:"commitTypesPercent"`
}

func mcpTimeline(dir string, args map[string]any) (any, error) {
	repos, byRepo, p, err := aiCommits(dir, args)
	if err != nil {
		return nil, err
	}
	commits, names := map[string][]Commit{}, map[string]map[string]bool{} // by month
	for _, r := range repos {
		for _, c := range byRepo[r.ID] {
			t, err := time.Parse(time.RFC3339, c.Date)
			if err != nil || !p.has(t.Local()) {
				continue
			}
			k := t.Local().Format("2006-01")
			commits[k] = append(commits[k], c)
			if names[k] == nil {
				names[k] = map[string]bool{}
			}
			names[k][r.Name] = true
		}
	}
	list := []month{}
	for _, k := range slices.Sorted(maps.Keys(commits)) {
		s := summarize(commits[k], p, false) // counted as work counts them
		list = append(list, month{k, s.Commits, slices.Sorted(maps.Keys(names[k])), s.Languages, s.CommitTypes})
	}
	return map[string]any{"months": list}, nil
}

var (
	trailerLine = regexp.MustCompile(`(?i)^(co-authored-by|signed-off-by|reviewed-by|acked-by|tested-by|reported-by|cc|change-id):`)
	emailLike   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	hashLike    = regexp.MustCompile(`\b[0-9a-f]{40}(?:[0-9a-f]{24})?\b`) // what git revert and cherry-pick -x write
)

// messageParts is a stored (already masked) commit message as AI tools may see it: the subject, and
// the body without trailers (who co-wrote or signed off) and without anything that looks like an email
// or a full commit hash.
func messageParts(msg string) (subject, body string) {
	subject, rest, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	var keep []string
	for _, l := range strings.Split(rest, "\n") {
		if !trailerLine.MatchString(strings.TrimSpace(l)) {
			keep = append(keep, l)
		}
	}
	clean := func(s string) string {
		return hashLike.ReplaceAllString(emailLike.ReplaceAllString(strings.TrimSpace(s), "[email]"), "[hash]")
	}
	return clean(subject), clean(strings.Join(keep, "\n"))
}

func mcpCommits(dir string, args map[string]any) (any, error) {
	if want, _ := args["repository"].(string); want == "" {
		return nil, errors.New("repository is required: one of those status shows")
	}
	repos, byRepo, p, err := aiCommits(dir, args)
	if err != nil {
		return nil, err
	}
	r := repos[0]
	if r.AI.Messages == "" {
		return nil, fmt.Errorf("%s does not show its commit messages to AI tools. The user can allow it: "+
			"gitfolio config ai messages <repository> subjects (or full), or set_ai_exposure when they ask", r.Name)
	}
	kind, _ := args["type"].(string)
	limit, offset := 100, 0
	if v, ok := args["limit"].(float64); ok {
		limit = min(max(int(v), 1), 500)
	}
	if v, ok := args["offset"].(float64); ok {
		offset = max(int(v), 0)
	}
	type entry struct {
		Date      string   `json:"date"`
		Type      string   `json:"type"`
		WorkKinds []string `json:"workKinds,omitempty"`
		Added     int      `json:"linesAdded"`
		Deleted   int      `json:"linesDeleted"`
		WithAI    bool     `json:"withAI,omitempty"`
		Branch    string   `json:"branch,omitempty"`
		Subject   string   `json:"subject"`
		Body      string   `json:"body,omitempty"`
	}
	var list []entry
	commits := slices.Clone(byRepo[r.ID])
	slices.SortStableFunc(commits, func(a, b Commit) int { return compareRFC3339(b.Date, a.Date) }) // newest first
	for _, c := range commits {
		t, err := time.Parse(time.RFC3339, c.Date)
		typ := cmp.Or(commitKinds[commitType(c.Message)], "other")
		if err != nil || !p.has(t.Local()) || (kind != "" && typ != kind) {
			continue
		}
		e := entry{Date: t.Local().Format(time.DateOnly), Type: typ, WithAI: c.CreationType != "" && c.CreationType != creationHuman}
		kinds := map[string]bool{}
		for _, f := range c.Files {
			e.Added, e.Deleted = e.Added+f.Add, e.Deleted+f.Del
			if k := workKind(f.Name); k != "" {
				kinds[k] = true
			}
		}
		e.WorkKinds = slices.Sorted(maps.Keys(kinds))
		if r.AI.Branches {
			e.Branch = c.Branch
		}
		e.Subject, e.Body = messageParts(c.Message)
		if r.AI.Messages != "full" {
			e.Body = ""
		}
		list = append(list, e)
	}
	total := len(list)
	list = list[min(offset, total):min(offset+limit, total)]
	res := map[string]any{"repository": r.Name, "messages": r.AI.Messages, "total": total, "offset": offset, "commits": list}
	if offset+len(list) < total {
		res["nextOffset"] = offset + len(list)
	}
	return res, nil
}

// compareRFC3339 orders two commit dates by the time they stand for, whatever their time zones.
func compareRFC3339(a, b string) int {
	x, _ := time.Parse(time.RFC3339, a)
	y, _ := time.Parse(time.RFC3339, b)
	return x.Compare(y)
}
