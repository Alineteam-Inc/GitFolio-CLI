package main

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The tech stack is worked out on this computer as aline.team works it out from what GitFolio sends:
// dependency names to techs with aline.team's rules (techstack_rules.go), techs a file's presence shows,
// and languages by file extension (languages). The developer type stays aline.team's.

type techInfo struct{ name, category string }

// safeDepName is the dependency names aline.team takes; others (spaces, quotes, line breaks) are dropped.
var safeDepName = regexp.MustCompile(`^[A-Za-z0-9@/._:+~$^-]{1,214}$`)

var (
	goRequire      = regexp.MustCompile(`(?i)^\s*([\w\-./]+(?:/v\d+)?)\s+v[\d.]+`)
	cargoDep       = regexp.MustCompile(`(?i)^\s*([a-zA-Z0-9_\-]+)\s*=`)
	gradleLine     = regexp.MustCompile(`(?i)(?:implementation|api|compile|runtimeOnly|compileOnly|testImplementation)\s*['"]([^:'"]+):([^:'"]+)(?::([^'"]+))?['"]`)
	pipEditable    = regexp.MustCompile(`(?i)^\s*-e\s+.*#egg=([a-zA-Z0-9_\-.]+)`)
	pipRequirement = regexp.MustCompile(`(?i)^\s*([a-zA-Z0-9_\-\.]+)`)
	condaItem      = regexp.MustCompile(`^\s*-\s*"?(?:[a-zA-Z0-9_.\-/]+::)?([a-zA-Z0-9][a-zA-Z0-9_.\-]*)`)
	gemLine        = regexp.MustCompile(`(?i)^\s*gem\s+['"]([^'"]+)['"]`)
	pubDep         = regexp.MustCompile(`(?i)^\s+([a-z0-9_]+):`)
	swiftURL       = regexp.MustCompile(`(?i)\.package\s*\(\s*url\s*:\s*['"]([^'"]+)['"]`)
	githubRepo     = regexp.MustCompile(`(?i)github\.com[/:]([^/]+)/([^/\.]+)`)
	hexDep         = regexp.MustCompile(`(?i)\{:([a-z_][a-z0-9_]*)`)
	denoModule     = regexp.MustCompile(`(deno\.land/x/[a-z0-9_]+|@[a-z0-9_-]+/[a-z0-9_-]+)`)
	sbtDep         = regexp.MustCompile(`"([a-zA-Z0-9_.\-]+)"\s*%{1,3}\s*"([a-zA-Z0-9_.\-]+)"`)
)

// depTechs are the techs one ecosystem's dependency names show: npm, go, maven (group:artifact), pypi,
// cargo, conda, composer (vendor/package), rubygems, pub, swift (package URL), cocoapods, hex, nuget
// (package or assembly; "sdk:" and the project Sdk), deno (import map key or value), sbt (group:artifact).
// Each name is read the way aline.team reads the names GitFolio sends: the same filter, then the line
// that ecosystem's manifest would have for it, read with the same patterns, so a name gives here exactly
// what it gives there (testdata/stack-golden.json).
func depTechs(ecosystem string, names []string) map[string]bool {
	techs := map[string]bool{}
	add := func(t string) {
		if t != "" {
			techs[t] = true
		}
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !safeDepName.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		switch ecosystem {
		case "npm":
			add(npmTechs[name])
		case "go":
			if m := goRequire.FindStringSubmatch(name + " v0.0.0"); m != nil {
				add(goTechs[m[1]])
			}
		case "cargo":
			if m := cargoDep.FindStringSubmatch(name + ` = "*"`); m != nil {
				add(cargoTechs[strings.ToLower(m[1])])
			}
		case "maven":
			for _, m := range gradleLine.FindAllStringSubmatch("implementation '"+name+":0'", -1) {
				if !strings.HasPrefix(m[1], "$") { // a Gradle variable
					add(jvmGroupTech(m[1]))
					for _, t := range jvmArtifactTechs(m[1], m[2]) {
						add(t)
					}
				}
			}
		case "sbt":
			group, artifact, ok := strings.Cut(name, ":")
			if !ok {
				continue
			}
			for _, m := range sbtDep.FindAllStringSubmatch(`libraryDependencies += "`+group+`" % "`+artifact+`" % "0"`, -1) {
				if t := scalaGroupTechs[m[1]]; t != "" {
					add(t)
					continue
				}
				add(jvmGroupTech(m[1]))
				for _, t := range jvmArtifactTechs(m[1], m[2]) {
					add(t)
				}
			}
		case "conda":
			if m := condaItem.FindStringSubmatch("  - " + name); m != nil && !strings.EqualFold(m[1], "pip") && !strings.EqualFold(m[1], "python") {
				add(cmp.Or(condaTechs[strings.ToLower(m[1])], pypiTechs[pypiName(m[1])]))
			}
		case "composer":
			add(composerTechs[name])
		case "rubygems":
			if m := gemLine.FindStringSubmatch("gem '" + name + "'"); m != nil {
				add(gemTechs[strings.ToLower(m[1])])
			}
		case "pub":
			if m := pubDep.FindStringSubmatch("  " + name + ": any"); m != nil {
				add(pubTechs[strings.ToLower(m[1])])
			}
		case "swift":
			if m := swiftURL.FindStringSubmatch(`.package(url: "` + name + `", from: "0.0.0"),`); m != nil {
				if g := githubRepo.FindStringSubmatch(m[1]); g != nil {
					add(swiftTechs[strings.ReplaceAll(strings.ToLower(g[2]), ".git", "")])
				}
			}
		case "cocoapods":
			add("IOS_NATIVE") // any pod: an iOS app
		case "hex":
			for _, m := range hexDep.FindAllStringSubmatch("{:"+name+`, "~> 0.0"},`, -1) {
				add(hexTechs[strings.ToLower(m[1])])
			}
		case "nuget":
			if sdk, ok := strings.CutPrefix(name, "sdk:"); ok {
				add(nugetSdkTechs[sdk])
			} else {
				add(nugetTechs[name])
			}
		case "deno":
			lower := strings.ToLower(name)
			if t := denoKeyTechs[lower]; t != "" {
				add(t)
			} else if m := denoModule.FindStringSubmatch(lower); m != nil {
				add(denoModuleTechs[m[1]])
			}
		case "pypi":
			if strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "-e") {
				continue // an option line in requirements.txt
			}
			pkg := ""
			if m := pipEditable.FindStringSubmatch(name); m != nil {
				pkg = m[1]
			} else if m := pipRequirement.FindStringSubmatch(name); m != nil {
				pkg = m[1]
			}
			add(pypiTechs[pypiName(pkg)])
		}
	}
	if techs["SPRING_BOOT"] { // Spring Framework comes with Spring Boot: not a tech of its own there
		delete(techs, "SPRING_FRAMEWORK")
	}
	return techs
}

// pypiName is a PyPI name normalized as PEP 503 does: lower case, _ and . as -.
func pypiName(name string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
}

// jvmArtifactTechs are the techs an artifact of a group adds, by artifact ID prefix.
func jvmArtifactTechs(group, artifact string) []string {
	var techs []string
	for _, r := range jvmArtifactRules {
		if r.group == group && strings.HasPrefix(artifact, r.artifactPrefix) {
			techs = append(techs, r.techs...)
		}
	}
	return techs
}

// jvmGroupTech is the tech of a Maven group ID: an exact rule first, then the first prefix rule that
// fits. A rule with no tech decides nothing, so a broader prefix after it does not apply either.
func jvmGroupTech(group string) string {
	if t, ok := jvmGroupTechs[group]; ok {
		return t
	}
	for _, p := range jvmGroupPrefixes {
		if strings.HasPrefix(group, p[0]) {
			return p[1]
		}
	}
	return ""
}

// filePresenceTechs are the techs a file's presence in a repository shows (a Dockerfile shows Docker), by
// its path in the repository. In a folder of other people's code or samples (skipDirs), or under an
// android/ or ios/ folder (Flutter and React Native make those), its name shows none; its extension
// (.vue) still does.
func filePresenceTechs(file string) []string {
	segments := strings.Split(strings.ToLower(strings.ReplaceAll(file, `\`, "/")), "/")
	base := segments[len(segments)-1]
	var techs []string
	switch {
	case inSkipDir(file) || slices.ContainsFunc(segments[:len(segments)-1], func(s string) bool { return s == "android" || s == "ios" }):
	case presenceTechs[base] != nil:
		techs = slices.Clone(presenceTechs[base])
	case strings.HasPrefix(base, "dockerfile") && !prefixBlockingExts[extension(file)]: // not dockerfile.md
		techs = []string{"DOCKER"}
	case strings.HasSuffix(base, ".tf"):
		techs = []string{"TERRAFORM"}
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 && presenceExtTechs[base[i+1:]] != "" {
		techs = append(techs, presenceExtTechs[base[i+1:]])
	}
	return techs
}

// techClass is which list of the tech stack a category goes to: LANGUAGE, FRAMEWORK, or LIBRARY for the
// rest (libraries, tools, protocols, databases).
func techClass(category string) string {
	if category == "LANGUAGE" || category == "FRAMEWORK" {
		return category
	}
	return "LIBRARY"
}

// knownFile reports whether aline.team knows the kind of a file (code, markup, data, prose, media, a
// package file), by its name or extension. (A code, prose or media file whose name starts with Dockerfile
// is of that kind, not a package file, but known either way.)
func knownFile(file string) bool {
	base := file[strings.LastIndexAny(file, `/\`)+1:]
	lower := strings.ToLower(base)
	if knownFileNames[base] || strings.HasPrefix(lower, "dockerfile") || strings.HasPrefix(lower, "makefile") {
		return true
	}
	i := strings.LastIndexByte(base, '.')
	return i > 0 && knownFileExts[strings.ToLower(base[i+1:])]
}

// localStack is the tech stack of the registered repositories from this computer's data, as aline.team
// works it out per repository: each tech the repository shows (its languages and the techs its files'
// presence show, from every file of the user's commits, and its approved dependencies) counts the
// repository once, with the span of the user's commits in it and the months they fall in (UTC days;
// merges and other system commits, commits with no message aline.team can tell, and files of a kind it
// does not know left out). Per tech: projectCount the repositories, startDate and endDate the first and
// last day, careerMonths the spans merged where they overlap or touch, activityMonths the months. Each
// list goes by projectCount, then name.
func localStack(repos []Repo, commits []Commit, deps []Dependency) techUsages {
	data := repoTechsOf(repos, commits, deps)
	type acc struct {
		projects   int
		ranges     [][2]time.Time
		start, end time.Time
		months     map[string]bool
	}
	byTech := map[string]*acc{}
	for _, r := range repos {
		d := data[r.ID]
		for k := range d.techs {
			if retiredTechs[k] {
				continue
			}
			k = cmp.Or(mergedTechs[k], k)
			a := byTech[k]
			if a == nil {
				a = &acc{months: map[string]bool{}}
				byTech[k] = a
			}
			a.projects++
			if !d.start.IsZero() {
				a.ranges = append(a.ranges, [2]time.Time{d.start, d.end})
				if a.start.IsZero() || d.start.Before(a.start) {
					a.start = d.start
				}
				if d.end.After(a.end) {
					a.end = d.end
				}
			}
			for m := range d.months {
				a.months[m] = true
			}
		}
	}
	u := techUsages{Languages: []techUsage{}, Frameworks: []techUsage{}, Libraries: []techUsage{}}
	for k, a := range byTech {
		info := techCatalog[k]
		t := techUsage{Name: info.name, ProjectCount: &a.projects, ActivityMonths: slices.Sorted(maps.Keys(a.months))}
		if len(a.ranges) > 0 {
			months := mergedMonths(a.ranges)
			t.StartDate, t.EndDate, t.CareerMonths = a.start.Format(localDateTime), a.end.Format(localDateTime), &months
		}
		switch techClass(info.category) {
		case "LANGUAGE":
			u.Languages = append(u.Languages, t)
		case "FRAMEWORK":
			u.Frameworks = append(u.Frameworks, t)
		default:
			u.Libraries = append(u.Libraries, t)
		}
	}
	for _, l := range [][]techUsage{u.Languages, u.Frameworks, u.Libraries} {
		slices.SortFunc(l, func(a, b techUsage) int {
			return cmp.Or(cmp.Compare(*b.ProjectCount, *a.ProjectCount), cmp.Compare(a.Name, b.Name))
		})
	}
	return u
}

// repoTechs is what one repository shows for the tech stack (localStack): its techs (catalog keys) and the
// span and months of the user's commits in it that count.
type repoTechs struct {
	techs      map[string]bool
	start, end time.Time
	months     map[string]bool
}

// repoTechsOf works out repoTechs for each repository, by its ID.
func repoTechsOf(repos []Repo, commits []Commit, deps []Dependency) map[string]*repoTechs {
	data := map[string]*repoTechs{}
	for _, r := range repos {
		data[r.ID] = &repoTechs{techs: map[string]bool{}, months: map[string]bool{}}
	}
	for _, c := range commits {
		d := data[c.Repo]
		if d == nil {
			continue
		}
		if t, ok := countsForStack(c); ok {
			if d.start.IsZero() || t.Before(d.start) {
				d.start = t
			}
			if t.After(d.end) {
				d.end = t
			}
			d.months[t.Format("2006-01")] = true
		}
		for _, f := range c.Files {
			for _, k := range fileTechs(f.Name) {
				d.techs[k] = true
			}
		}
	}
	names := map[[2]string][]string{} // repository and ecosystem → dependency names
	for _, dep := range deps {
		names[[2]string{dep.Repo, dep.Ecosystem}] = append(names[[2]string{dep.Repo, dep.Ecosystem}], dep.Name)
	}
	for key, n := range names {
		if d := data[key[0]]; d != nil {
			for k := range depTechs(key[1], n) {
				d.techs[k] = true
			}
		}
	}
	return data
}

// countsForStack is the UTC day of a commit that counts for the tech stack's spans: not a merge or other
// system commit, not one whose kind cannot be told, and with a file of a kind aline.team knows.
func countsForStack(c Commit) (time.Time, bool) {
	typ := commitType(c.Message)
	t, err := time.Parse(time.RFC3339, c.Date)
	if err != nil || typ == "SYSTEM" || typ == "UNCATEGORIZED" || !slices.ContainsFunc(c.Files, func(f FileStat) bool { return knownFile(f.Name) }) {
		return time.Time{}, false
	}
	return t.UTC().Truncate(24 * time.Hour), true
}

// languageKeys maps a language's display name to its catalog key.
var languageKeys = func() map[string]string {
	keys := map[string]string{}
	for k, t := range techCatalog {
		if t.category == "LANGUAGE" {
			keys[t.name] = k
		}
	}
	return keys
}()

// fileTechs is the techs (catalog keys) a file shows: its language and the techs its presence shows.
func fileTechs(file string) []string {
	techs := filePresenceTechs(file)
	if k := languageKeys[languageOf(file)]; k != "" {
		techs = append(techs, k)
	}
	return techs
}

// localDateTime is how aline.team writes a date and time without a zone.
const localDateTime = "2006-01-02T15:04:05"

// mergedMonths is the months the spans cover, overlapping or touching ones merged first: whole months
// each, as java.time counts them (a month counts once its day and time of day are reached).
func mergedMonths(spans [][2]time.Time) int {
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	total, cur := 0, spans[0]
	for _, s := range spans[1:] {
		if !s[0].After(cur[1]) {
			if s[1].After(cur[1]) {
				cur[1] = s[1]
			}
			continue
		}
		total += monthsBetween(cur[0], cur[1])
		cur = s
	}
	return total + monthsBetween(cur[0], cur[1])
}

// monthsBetween is java.time's whole months from start to end (end not before start).
func monthsBetween(start, end time.Time) int {
	day := func(t time.Time) time.Duration { // time of day
		return t.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()))
	}
	y, m, d := end.Date()
	if end.After(start) && day(end) < day(start) && (y != start.Year() || m != start.Month() || d != start.Day()) {
		y, m, d = end.AddDate(0, 0, -1).Date()
	}
	packed := func(y int, m time.Month, d int) int { return (y*12+int(m)-1)*32 + d }
	return (packed(y, m, d) - packed(start.Date())) / 32
}
