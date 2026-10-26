package main

import (
	"bufio"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// manifestNames maps the supported package manager files to their ecosystem (DESIGN 3.5).
var manifestNames = map[string]string{
	"package.json":       "npm",
	"go.mod":             "go",
	"requirements.txt":   "pypi",
	"pyproject.toml":     "pypi",
	"Pipfile":            "pypi",
	"pom.xml":            "maven",
	"build.gradle":       "maven",
	"build.gradle.kts":   "maven",
	"libs.versions.toml": "maven",
	"Cargo.toml":         "cargo",
	"environment.yml":    "conda",
	"composer.json":      "composer",
	"Gemfile":            "rubygems",
	"pubspec.yaml":       "pub",
	"Package.swift":      "swift",
	"Podfile":            "cocoapods",
	"mix.exs":            "hex",
	"deno.json":          "deno",
	"build.sbt":          "sbt",
}

// manifestEcosystem is the ecosystem of a package manager file by its path, "" for none: by name, and
// .NET project files by extension.
func manifestEcosystem(file string) string {
	base := path.Base(file)
	switch path.Ext(base) {
	case ".csproj", ".vbproj", ".fsproj":
		return "nuget"
	}
	return manifestNames[base]
}

// skipDirs hold other people's code or samples, as aline.team skips them: manifest files inside are not
// even offered, and files inside show no tech by their presence.
var skipDirs = []string{"node_modules", "vendor", "bower_components", "third_party", "testdata", "fixtures", "examples", "example"}

// inSkipDir reports whether a folder of repository path p (in any case) is one of skipDirs.
func inSkipDir(p string) bool {
	dirs := strings.Split(strings.ToLower(strings.ReplaceAll(p, `\`, "/")), "/")
	return slices.ContainsFunc(dirs[:len(dirs)-1], func(d string) bool { return slices.Contains(skipDirs, d) })
}

const maxManifests = 200

// Dependency is one declared dependency of an approved manifest file.
type Dependency struct {
	Repo      string `json:"repo"`                // Repo.ID locally, repository name in export
	Provider  string `json:"provider,omitempty"`  // filled in export only
	Namespace string `json:"namespace,omitempty"` // filled in export only
	Module    string `json:"module,omitempty"`    // local only: matches FileStat.Module, never exported
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
}

// dep is one dependency a manifest declares; eco, when set, is its ecosystem instead of the manifest's
// (environment.yml declares conda and pip packages), and local marks one from a local path, a git
// repository or another address.
type dep struct {
	name, version, eco string
	local              bool
}

func loadDeps(dir string) (deps []Dependency, err error) {
	err = loadJSON(filepath.Join(dir, "deps.json"), &deps)
	return deps, err
}

// manifestCandidates lists supported manifest files committed at HEAD. It looks at file names only.
func manifestCandidates(repo string) []string {
	out, err := git(repo, "ls-tree", "-r", "-z", "--name-only", "HEAD")
	if err != nil {
		return nil // no commits yet
	}
	all := strings.Split(out, "\x00")
	var files []string
	for _, p := range all {
		if manifestEcosystem(p) == "" || inSkipDir(p) || path.Base(p) == "Podfile" && crossPlatformIOS(p, all) {
			continue
		}
		if len(files) == maxManifests {
			warn(detectLang(os.Getenv), "tooManyManifests", maxManifests)
			break
		}
		files = append(files, p)
	}
	return files
}

// pendingManifests counts manifest files the user has not decided on yet.
func pendingManifests(r Repo) int {
	n := 0
	for _, p := range manifestCandidates(r.Path) {
		if _, decided := r.Manifests[p]; !decided {
			n++
		}
	}
	return n
}

func moduleID(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:4])
}

// moduleOf returns the module of the nearest directory above p that holds an approved manifest.
func moduleOf(p string, modules map[string]string) string {
	if len(modules) == 0 {
		return ""
	}
	for d := manifestDir(p); ; d = manifestDir(d) {
		if id, ok := modules[d]; ok || d == "" {
			return id
		}
	}
}

// manifestDir is the module directory of manifest file p ("" for the repository root).
func manifestDir(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

// manifestModules maps the directories of r's approved manifest files to module IDs. It reads no file:
// collecting commits only needs to know which module each changed file belongs to.
func manifestModules(r Repo) map[string]string {
	modules := map[string]string{}
	for p, ok := range r.Manifests {
		if ok {
			modules[manifestDir(p)] = moduleID(manifestDir(p))
		}
	}
	return modules
}

// refreshDeps reads only the approved manifest files of r (committed version, file contents are
// parsed in memory and dropped) and stores their dependencies. It runs only when asked: `deps scan`,
// right after approving files, and scan and sync with `deps auto on` (never on push).
func refreshDeps(dir string, r *Repo) (files, deps int, err error) {
	var found []Dependency
	for p, ok := range r.Manifests {
		if !ok {
			continue
		}
		content, err := git(r.Path, "cat-file", "-p", "HEAD:"+p)
		if err != nil {
			continue // removed since it was approved
		}
		files++
		for _, x := range parseManifest(path.Base(p), content) {
			found = append(found, Dependency{Repo: r.ID, Module: moduleID(manifestDir(p)), Ecosystem: cmp.Or(x.eco, manifestEcosystem(p)), Name: x.name, Version: x.version})
		}
	}
	all, err := loadDeps(dir)
	if err != nil {
		return 0, 0, err
	}
	all = append(slices.DeleteFunc(all, func(x Dependency) bool { return x.Repo == r.ID }), found...)
	slices.SortFunc(all, func(a, b Dependency) int {
		return strings.Compare(a.Repo+a.Module+a.Ecosystem+a.Name, b.Repo+b.Module+b.Ecosystem+b.Name)
	})
	return files, len(found), saveJSON(filepath.Join(dir, "deps.json"), all)
}

// parseManifest extracts directly declared dependencies. Lock files, local paths, git and URL
// dependencies are left out.
func parseManifest(name, s string) []dep {
	var deps []dep
	switch name {
	case "package.json":
		deps = parsePackageJSON(s)
	case "go.mod":
		deps = parseGoMod(s)
	case "requirements.txt":
		for _, line := range strings.Split(s, "\n") {
			if m := pipEditable.FindStringSubmatch(line); m != nil {
				deps = append(deps, dep{name: m[1], local: true}) // -e git+https://…#egg=name
				continue
			}
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			if d, ok := pep508(line); ok {
				deps = append(deps, d)
			}
		}
	case "pyproject.toml":
		deps = parsePyproject(s)
	case "Pipfile":
		section := ""
		for _, line := range strings.Split(s, "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "[") {
				section = strings.Trim(t, "[] ")
			} else if d, ok := tomlDep(t); ok && (section == "packages" || section == "dev-packages") {
				deps = append(deps, d)
			}
		}
	case "pom.xml":
		deps = parsePom(s)
	case "build.gradle", "build.gradle.kts":
		for _, g := range gradleDep.FindAllStringSubmatch(s, -1) {
			deps = append(deps, dep{name: g[1] + ":" + g[2], version: g[3]})
		}
	case "libs.versions.toml":
		deps = parseVersionCatalog(s)
	case "Cargo.toml":
		deps = parseCargo(s)
	case "environment.yml":
		deps = parseConda(s)
	case "composer.json":
		deps = parseComposer(s)
	case "Gemfile":
		deps = matchDeps(gemDecl, s)
	case "pubspec.yaml":
		deps = parsePubspec(s)
	case "Package.swift":
		for _, m := range swiftPackage.FindAllStringSubmatch(s, -1) {
			deps = append(deps, dep{name: m[1], version: m[2], local: true}) // an address
		}
	case "Podfile":
		deps = matchDeps(podDecl, s)
	case "mix.exs":
		deps = matchDeps(mixDep, s)
	case "deno.json":
		deps = parseDeno(s)
	case "build.sbt":
		for _, m := range sbtDecl.FindAllStringSubmatch(s, -1) {
			deps = append(deps, dep{name: m[1] + ":" + m[2], version: m[3]})
		}
	default:
		if manifestEcosystem(name) == "nuget" {
			deps = parseMsProj(s)
		}
	}
	// aline.team reads a local or git dependency by its name too. It is sent only when that name shows a
	// tech there, a public one's then, so the rest stay here; never a pod, as any pod shows iOS.
	eco := manifestEcosystem(name)
	deps = slices.DeleteFunc(deps, func(d dep) bool {
		return d.local && (eco == "cocoapods" || len(depTechs(cmp.Or(d.eco, eco), []string{d.name})) == 0)
	})
	slices.SortFunc(deps, func(a, b dep) int { return strings.Compare(a.name, b.name) })
	return slices.CompactFunc(deps, func(a, b dep) bool { return a.name == b.name })
}

func parsePackageJSON(s string) []dep {
	var pkg struct {
		Dependencies     map[string]string `json:"dependencies"`
		DevDependencies  map[string]string `json:"devDependencies"`
		PeerDependencies map[string]string `json:"peerDependencies"` // some frameworks (htmx, tRPC) are only peers
	}
	if json.Unmarshal([]byte(s), &pkg) != nil {
		return nil
	}
	var deps []dep
	for _, m := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies} {
		for n, v := range m {
			if npmLocal(v) {
				deps = append(deps, dep{name: n, local: true}) // its version is a path or an address
			} else {
				deps = append(deps, dep{name: n, version: v})
			}
		}
	}
	return deps
}

// npmLocal reports npm versions that point at local paths, workspaces, git or URLs instead of the registry.
func npmLocal(v string) bool {
	for _, p := range []string{"file:", "link:", "workspace:", "portal:", "git", "http:", "https:", "github:", ".", "/", "~/"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return strings.Contains(v, "/") && !strings.HasPrefix(v, "npm:") // "user/repo" GitHub shorthand
}

func parseGoMod(s string) []dep {
	var deps []dep
	block := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			block = true
			continue
		case block && line == ")":
			block = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !block:
			continue
		}
		if f := strings.Fields(line); len(f) >= 2 && !strings.Contains(line, "// indirect") {
			deps = append(deps, dep{name: f[0], version: f[1]})
		}
	}
	return deps
}

// pep508 splits a Python requirement such as `requests[socks]>=2.31; python_version>"3.8"`.
func pep508(s string) (dep, bool) {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return dep{}, false // options and local paths
	}
	if name, _, ok := strings.Cut(s, "@"); ok { // name @ URL
		name, _, _ = strings.Cut(name, "[")
		name = strings.TrimSpace(name)
		return dep{name: name, local: true}, pyName.MatchString(name)
	}
	if strings.Contains(s, "://") {
		return dep{}, false // a URL
	}
	name, version := s, ""
	if i := strings.IndexAny(s, "=<>!~[ "); i >= 0 {
		name = s[:i]
		if j := strings.IndexAny(s, "=<>!~"); j >= 0 {
			version = strings.TrimPrefix(strings.TrimSpace(s[j:]), "==")
		}
	}
	return dep{name: name, version: version}, name != ""
}

var pyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var quotedRe = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)

func quoted(s string) []string {
	var out []string
	for _, q := range quotedRe.FindAllStringSubmatch(s, -1) {
		out = append(out, q[1]+q[2])
	}
	return out
}

// tableKey returns key's string value in a TOML inline table such as `{ version = "1", path = "x" }`.
func tableKey(v, key string) string {
	if m := regexp.MustCompile(`(?:^|[\s{,])` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]*)"`).FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

// tomlDep reads `name = "1.0"` or `name = { version = "1.0" }`; path and git dependencies are skipped.
func tomlDep(line string) (dep, bool) {
	k, v, ok := strings.Cut(line, "=")
	if !ok || strings.HasPrefix(line, "#") {
		return dep{}, false
	}
	name := strings.Trim(strings.TrimSpace(k), `"'`)
	name, _, _ = strings.Cut(name, ".") // serde.workspace = true
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "{") {
		if tableKey(v, "path") != "" || tableKey(v, "git") != "" {
			return dep{name: name, local: true}, name != ""
		}
		return dep{name: name, version: tableKey(v, "version")}, name != ""
	}
	version := ""
	if q := quoted(v); len(q) > 0 {
		version = q[0]
	}
	return dep{name: name, version: version}, name != ""
}

func parsePyproject(s string) []dep {
	var deps []dep
	section, inList := "", false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if !inList && section == "project" && strings.HasPrefix(t, "dependencies") {
			_, t, _ = strings.Cut(t, "=")
			inList = true
		}
		if inList {
			for _, q := range quoted(t) {
				if d, ok := pep508(q); ok {
					deps = append(deps, d)
				}
			}
			if strings.Contains(quotedRe.ReplaceAllString(t, ""), "]") {
				inList = false
			}
			continue
		}
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[] ")
			continue
		}
		poetry := section == "tool.poetry.dependencies" ||
			strings.HasPrefix(section, "tool.poetry.group.") && strings.HasSuffix(section, ".dependencies")
		if d, ok := tomlDep(t); poetry && ok && d.name != "python" {
			deps = append(deps, d)
		}
	}
	return deps
}

func parseCargo(s string) []dep {
	isDeps := func(section string) bool {
		last := section[strings.LastIndexByte(section, '.')+1:]
		return last == "dependencies" || last == "dev-dependencies" || last == "build-dependencies"
	}
	var deps []dep
	var table *dep // [dependencies.serde] describes one dependency in the lines below
	section := ""
	flush := func() {
		if table != nil {
			deps = append(deps, *table)
		}
		table = nil
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			flush()
			section = strings.Trim(t, "[] ")
			if i := strings.LastIndexByte(section, '.'); i > 0 && isDeps(section[:i]) {
				table = &dep{name: section[i+1:]}
			}
			continue
		}
		if table != nil {
			if k, v, ok := strings.Cut(t, "="); ok {
				switch strings.TrimSpace(k) {
				case "version":
					if q := quoted(v); len(q) > 0 {
						table.version = q[0]
					}
				case "path", "git":
					table.local = true
				}
			}
			continue
		}
		if d, ok := tomlDep(t); ok && isDeps(section) {
			deps = append(deps, d)
		}
	}
	flush()
	return deps
}

func parsePom(s string) []dep {
	var pom struct {
		Deps []struct {
			Group    string `xml:"groupId"`
			Artifact string `xml:"artifactId"`
			Version  string `xml:"version"`
		} `xml:"dependencies>dependency"`
	}
	if xml.Unmarshal([]byte(s), &pom) != nil {
		return nil
	}
	var deps []dep
	for _, d := range pom.Deps {
		deps = append(deps, dep{name: d.Group + ":" + d.Artifact, version: d.Version})
	}
	return deps
}

var gradleDep = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|annotationProcessor|kapt|ksp|classpath|compile|testCompile)\s*\(?\s*(?:platform\s*\(\s*)?["']([^"':\s]+):([^"':\s]+)(?::([^"'\s]+))?["']`)

func parseVersionCatalog(s string) []dep {
	versions := map[string]string{}
	type lib struct{ name, version, ref string }
	var libs []lib
	section := ""
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[] ")
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || strings.HasPrefix(t, "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case section == "versions":
			if q := quoted(v); len(q) > 0 {
				versions[k] = q[0]
			}
		case section == "libraries" && strings.HasPrefix(v, "{"):
			name := tableKey(v, "module")
			if g := tableKey(v, "group"); name == "" && g != "" {
				name = g + ":" + tableKey(v, "name")
			}
			libs = append(libs, lib{name, tableKey(v, "version"), tableKey(v, "version.ref")})
		case section == "libraries":
			if q := quoted(v); len(q) > 0 {
				parts := strings.SplitN(q[0], ":", 3)
				if len(parts) == 3 {
					libs = append(libs, lib{parts[0] + ":" + parts[1], parts[2], ""})
				} else {
					libs = append(libs, lib{q[0], "", ""})
				}
			}
		}
	}
	var deps []dep
	for _, l := range libs {
		if l.version == "" {
			l.version = versions[l.ref]
		}
		if l.name != "" {
			deps = append(deps, dep{name: l.name, version: l.version})
		}
	}
	return deps
}

var stdin = bufio.NewReader(os.Stdin)

// interactive reports whether a person can answer prompts (not a hook, pipe or scheduled run).
var interactive = func() bool { return terminal(os.Stdin) } // a variable so tests can answer prompts

// stdinClosed turns true once input ends; some tools look like a terminal but cannot answer.
var stdinClosed bool

func prompt(q string) string {
	blank()
	lines := strings.Split(strings.TrimLeft(q, " \n"), "\n")
	for i, l := range lines {
		if i == 0 {
			lines[i] = margin + symQuestion + " " + styleQuestion(l, true)
		} else {
			lines[i] = margin + "  " + styleQuestion(l, false) // under the text after "? "
		}
	}
	fmt.Fprint(out, strings.Join(lines, "\n"))
	line, err := stdin.ReadString('\n')
	if err != nil {
		stdinClosed = true
		fmt.Fprintln(out) // no answer: keep the next output on its own line
	} else {
		answered()
	}
	return strings.TrimSpace(line)
}

// crossPlatformIOS reports whether a Podfile is in an ios/ folder that Flutter or React Native made:
// one whose parent folder has a pubspec.yaml or package.json. aline.team does not take its pods as an
// iOS app, so they are not offered.
func crossPlatformIOS(file string, files []string) bool {
	dirs := strings.Split(path.Dir(file), "/")
	for i, d := range dirs {
		if strings.EqualFold(d, "ios") {
			parent := strings.Join(dirs[:i], "/")
			if parent != "" {
				parent += "/"
			}
			if slices.Contains(files, parent+"pubspec.yaml") || slices.Contains(files, parent+"package.json") {
				return true
			}
		}
	}
	return false
}

var (
	gemDecl      = regexp.MustCompile(`(?m)^\s*gem\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?([^\n]*)`)
	podDecl      = regexp.MustCompile(`(?m)^\s*pod\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?([^\n]*)`)
	mixDep       = regexp.MustCompile(`\{:([a-z_][a-z0-9_]*)\s*(?:,\s*"([^"]+)")?([^}]*)\}`)
	swiftPackage = regexp.MustCompile(`\.package\s*\(\s*(?:name\s*:\s*"[^"]*"\s*,\s*)?url\s*:\s*"([^"]+)"(?:\s*,\s*(?:from|exact|branch|revision)\s*:\s*"([^"]*)")?`)
	sbtDecl      = regexp.MustCompile(`"([^"\s]+)"\s*%{1,3}\s*"([^"\s]+)"\s*%\s*"([^"]*)"`)
	localDep     = regexp.MustCompile(`\b(?:path|git|github|in_umbrella)\s*(?::|=>)`)
)

// matchDeps are the name, version and options a Gemfile, Podfile or mix.exs pattern finds; ones taken
// from a local path or a git repository are local.
func matchDeps(re *regexp.Regexp, s string) []dep {
	var deps []dep
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		deps = append(deps, dep{name: m[1], version: m[2], local: localDep.MatchString(m[3])})
	}
	return deps
}

// parseConda reads an environment.yml's dependencies: conda packages (a channel prefix like
// "conda-forge::" dropped), and the PyPI ones of its "- pip:" list.
func parseConda(s string) []dep {
	var deps []dep
	in, pipIndent := false, -1
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
			continue
		case indent == 0 && !strings.HasPrefix(t, "-"):
			in, pipIndent = t == "dependencies:", -1
			continue
		case !in || !strings.HasPrefix(t, "-"):
			continue
		}
		item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "-")), `"'`)
		if pipIndent >= 0 && indent > pipIndent {
			if d, ok := pep508(item); ok {
				d.eco = "pypi"
				deps = append(deps, d)
			}
			continue
		}
		pipIndent = -1
		if item == "pip:" {
			pipIndent = indent
			continue
		}
		if _, after, ok := strings.Cut(item, "::"); ok {
			item = after
		}
		i := strings.IndexAny(item, "=<>!~ ")
		if i < 0 {
			i = len(item)
		}
		if name := item[:i]; name != "" && name != "python" && name != "pip" {
			deps = append(deps, dep{name: name, version: strings.TrimLeft(item[i:], "=<>!~ ")})
		}
	}
	return deps
}

// parseComposer reads a composer.json's require and require-dev ("vendor/package"); PHP itself and its
// extensions are not packages.
func parseComposer(s string) []dep {
	var c struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if json.Unmarshal([]byte(s), &c) != nil {
		return nil
	}
	var deps []dep
	for _, m := range []map[string]string{c.Require, c.RequireDev} {
		for n, v := range m {
			if strings.Contains(n, "/") {
				deps = append(deps, dep{name: n, version: v})
			}
		}
	}
	return deps
}

// parsePubspec reads a pubspec.yaml's dependencies and dev_dependencies (Flutter's SDK ones too); ones
// from a local path or git are local.
func parsePubspec(s string) []dep {
	var deps []dep
	section, cur := "", dep{}
	flush := func() {
		if cur.name != "" {
			deps = append(deps, cur)
		}
		cur = dep{}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
		case indent == 0:
			flush()
			section = strings.TrimSuffix(t, ":")
		case section != "dependencies" && section != "dev_dependencies":
		case indent == 2:
			flush()
			name, v, _ := strings.Cut(t, ":")
			cur = dep{name: name, version: strings.Trim(strings.TrimSpace(v), `"'`)}
		case strings.HasPrefix(t, "path:") || strings.HasPrefix(t, "git:"):
			cur.local = true
		}
	}
	flush()
	return deps
}

// parseDeno reads a deno.json's import map (and its scopes): every key, and of each value (an address)
// only the module id in it that aline.team reads (deno.land/x/…, @scope/name).
func parseDeno(s string) []dep {
	var d struct {
		Imports map[string]string            `json:"imports"`
		Scopes  map[string]map[string]string `json:"scopes"`
	}
	if json.Unmarshal([]byte(s), &d) != nil {
		return nil
	}
	var deps []dep
	for _, m := range append([]map[string]string{d.Imports}, slices.Collect(maps.Values(d.Scopes))...) {
		for k, v := range m {
			deps = append(deps, dep{name: k})
			if id := denoModule.FindString(strings.ToLower(v)); id != "" {
				deps = append(deps, dep{name: id, local: true})
			}
		}
	}
	return deps
}

// parseMsProj reads a .NET project file: its Sdk (as "sdk:Microsoft.NET.Sdk.Web"), and the Include of
// each PackageReference and Reference (an assembly's version part dropped).
func parseMsProj(s string) []dep {
	var deps []dep
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		tok, err := dec.Token()
		if err != nil {
			return deps
		}
		e, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(name string) string {
			for _, a := range e.Attr {
				if a.Name.Local == name {
					return a.Value
				}
			}
			return ""
		}
		switch e.Name.Local {
		case "Project":
			if sdk := attr("Sdk"); sdk != "" {
				deps = append(deps, dep{name: "sdk:" + sdk})
			}
		case "PackageReference", "Reference":
			if name, _, _ := strings.Cut(attr("Include"), ","); strings.TrimSpace(name) != "" {
				deps = append(deps, dep{name: strings.TrimSpace(name), version: attr("Version")})
			}
		}
	}
}

// parseSelection turns "all", "none" or "1,3,5-7" into sorted numbers between 1 and n.
func parseSelection(s string, n int) ([]int, error) {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "all":
		sel := make([]int, n)
		for i := range sel {
			sel[i] = i + 1
		}
		return sel, nil
	case "none":
		return nil, nil
	}
	var sel []int
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := a, error(nil)
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("%q is not a valid choice (use all, none, or numbers like 1,3,5-7 between 1 and %d)", part, n)
		}
		for i := a; i <= b; i++ {
			sel = append(sel, i)
		}
	}
	slices.Sort(sel)
	return slices.Compact(sel), nil
}

// reviewManifests asks once which package manager files of rs may be read: the files of all of them
// in one list, grouped by repository and numbered across it. It updates rs in place and returns the
// indexes of the repositories whose decisions changed; Enter keeps every decision as it is.
func reviewManifests(rs []Repo) []int {
	lang := detectLang(os.Getenv)
	files := make([][]string, len(rs))
	var none []string
	n := 0
	sideBySide(len(rs), func(i int) { files[i] = manifestCandidates(rs[i].Path) })
	for i, r := range rs {
		if len(files[i]) == 0 {
			none = append(none, r.Name)
		}
		n += len(files[i])
	}
	if len(none) > 0 {
		say(lang, "noManifests", strings.Join(none, ", "))
	}
	if n == 0 {
		return nil
	}
	notice(tr(lang, "manifestsTitle"))
	k := 0
	for i, r := range rs {
		if len(files[i]) > 0 {
			fmt.Fprintf(out, "%s  %s\n", margin, bold(r.Name))
		}
		for _, f := range files[i] {
			k++
			state := tr(lang, "manifestNew")
			if ok, decided := r.Manifests[f]; decided {
				state = tr(lang, map[bool]string{true: "manifestAllowed", false: "manifestDeclined"}[ok])
			}
			fmt.Fprintf(out, "%s    %3d  %s  %s\n", margin, k, dim(pad(state, 8)), f)
		}
	}
	for {
		answer := prompt(tr(lang, "manifestsAsk"))
		if answer == "" {
			return nil
		}
		sel, err := parseSelection(answer, n)
		if err != nil {
			notice(fmt.Sprintf(tr(lang, "badSelection"), n))
			continue
		}
		var changed []int
		k = 0
		for i := range rs {
			if len(files[i]) == 0 {
				continue
			}
			m := map[string]bool{}
			for _, f := range files[i] {
				k++
				m[f] = slices.Contains(sel, k)
			}
			if !maps.Equal(m, rs[i].Manifests) {
				rs[i].Manifests = m
				changed = append(changed, i)
			}
		}
		return changed
	}
}

// cmdDeps shows or changes dependency detection; with --json it shows the state after, as depsJSON.
func cmdDeps(dir string, args []string) error {
	jsonMode(args)
	args = withoutJSON(args)
	if err := changeDeps(dir, args); err != nil || !jsonOut {
		return err
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	return depsJSON(cfg, repos)
}

// depsJSON is dependency detection for a program: on or off, deps auto, and each repository's package
// manager files with the user's decision: allowed, declined, or new (not decided yet).
func depsJSON(cfg Config, repos []Repo) error {
	type file struct {
		Path  string `json:"path"`
		State string `json:"state"`
	}
	type repo struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Files []file `json:"files"`
	}
	list, candidates := []repo{}, make([][]string, len(repos))
	sideBySide(len(repos), func(i int) { candidates[i] = manifestCandidates(repos[i].Path) })
	for i, r := range repos {
		files := []file{}
		for _, f := range candidates[i] {
			state := "new"
			if ok, decided := r.Manifests[f]; decided {
				state = map[bool]string{true: "allowed", false: "declined"}[ok]
			}
			files = append(files, file{f, state})
		}
		list = append(list, repo{r.Name, r.Path, files})
	}
	return printJSON(map[string]any{"on": cfg.Deps, "auto": cfg.DepsAuto, "repositories": list})
}

func changeDeps(dir string, args []string) error {
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	repos, err := loadRepos(dir)
	if err != nil {
		return err
	}
	lang := detectLang(os.Getenv)
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		if jsonOut {
			return nil // cmdDeps shows it
		}
		msg := fmt.Sprintf(tr(lang, "depsStatus"), tr(lang, map[bool]string{true: "on", false: "off"}[cfg.Deps]))
		msg += fmt.Sprintf(tr(lang, "depsAutoStatus"), tr(lang, map[bool]string{true: "on", false: "off"}[cfg.DepsAuto]))
		for _, r := range repos {
			allowed := 0
			for _, ok := range r.Manifests {
				if ok {
					allowed++
				}
			}
			msg += fmt.Sprintf(tr(lang, "depsRepo"), r.Name, allowed, pendingManifests(r))
		}
		show(out, settings(msg))
		return nil
	case "on":
		notice(tr(lang, "depsNotice"))
		cfg.Deps = true
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		if !interactive() {
			say(lang, "depsReviewLater")
			return nil
		}
		if err := reviewAndRescan(dir, repos); err != nil {
			return err
		}
		return saveRepos(dir, repos)
	case "off":
		cfg.Deps = false
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, "deps.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for i := range repos {
			repos[i].Manifests = nil
		}
		commits, err := readCommits(dir)
		if err != nil {
			return err
		}
		for i := range commits {
			for j := range commits[i].Files {
				commits[i].Files[j].Module = ""
			}
		}
		if err := writeCommits(dir, commits); err != nil {
			return err
		}
		say(lang, "depsOff")
		return saveRepos(dir, repos)
	case "review":
		if !cfg.Deps {
			return failure("depsIsOff")
		}
		var allow, rest []string // --allow <file>, any number of times: a program's choice
		for k := 1; k < len(args); k++ {
			switch a := args[k]; {
			case a == "--allow" && k+1 < len(args):
				allow, k = append(allow, args[k+1]), k+1
			case strings.HasPrefix(a, "--allow="):
				allow = append(allow, strings.TrimPrefix(a, "--allow="))
			case strings.HasPrefix(a, "-") || len(rest) > 0: // a typo would decline every file
				return wrongWord("deps review", a, []string{"--allow"}, "gitfolio deps review [path] [--allow <file>]... [--json]")
			default:
				rest = append(rest, a)
			}
		}
		top, err := topLevel(pathArg(rest))
		if err != nil {
			return err
		}
		i := slices.IndexFunc(repos, func(r Repo) bool { return r.Path == top })
		if i < 0 {
			return failure("notRegisteredAdd", tildePath(top))
		}
		if !jsonOut && len(allow) == 0 {
			if err := reviewAndRescan(dir, repos[i:i+1]); err != nil {
				return err
			}
			return saveRepos(dir, repos)
		}
		// Without questions: the files given with --allow may be read, every other one is declined.
		files := manifestCandidates(top)
		m := map[string]bool{}
		for _, f := range files {
			m[f] = slices.Contains(allow, f)
		}
		for _, f := range allow {
			if !slices.Contains(files, f) {
				return failure("manifestUnknown", f, repos[i].Name)
			}
		}
		if !maps.Equal(m, repos[i].Manifests) {
			repos[i].Manifests = m
			if err := rescanDeps(dir, &repos[i]); err != nil {
				return err
			}
		}
		return saveRepos(dir, repos)
	case "auto":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			break
		}
		cfg.DepsAuto = args[1] == "on"
		if err := saveConfig(dir, cfg); err != nil {
			return err
		}
		say(lang, "depsAuto", tr(lang, args[1]))
		if cfg.DepsAuto && !cfg.Deps {
			say(lang, "depsIsOff")
		}
		return nil
	case "scan":
		if !cfg.Deps {
			return failure("depsIsOff")
		}
		fs := flag.NewFlagSet("deps scan", flag.ContinueOnError)
		all := fs.Bool("all", false, "every registered repository")
		if err := parseFlags(fs, flagsFirst(args[1:]), "gitfolio deps scan [path] [--all]"); err != nil {
			return err
		}
		var top string
		if !*all {
			if top, err = topLevel(pathArg(fs.Args())); err != nil {
				return err
			}
		}
		matched := false
		for i := range repos {
			if !*all && repos[i].Path != top {
				continue
			}
			matched = true
			files, deps, err := refreshDeps(dir, &repos[i])
			if err != nil {
				warn(lang, "repoFailed", repos[i].Name, err)
				continue
			}
			say(lang, "depsScanned", repos[i].Name, files, deps)
		}
		if !*all && !matched {
			return failure("notRegisteredAdd", tildePath(top))
		}
		return nil
	}
	const usage = "gitfolio deps [on|off|review [path] [--allow <file>]...|scan [path] [--all]|auto on|off] [--json]"
	if len(args) > 1 && args[0] == "auto" {
		return wrongWord("deps auto", args[1], []string{"on", "off"}, usage)
	}
	return wrongWord("deps", args[0], []string{"on", "off", "review", "scan", "auto"}, usage)
}

// reviewAndRescan asks about the manifest files of rs in one list and updates the repositories whose
// decisions changed, so dependencies and module IDs match the new approvals.
func reviewAndRescan(dir string, rs []Repo) error {
	for _, i := range reviewManifests(rs) {
		if err := rescanDeps(dir, &rs[i]); err != nil {
			return err
		}
	}
	return nil
}

// rescanDeps puts r's stored commits in the modules of its new decisions and reads the approved files.
// The module of a file depends only on its path, which is stored as is: git history is not read again.
func rescanDeps(dir string, r *Repo) error {
	commits, err := readCommits(dir)
	if err != nil {
		return err
	}
	modules, n := manifestModules(*r), 0
	for _, c := range commits {
		if c.Repo != r.ID {
			continue
		}
		n++
		for i := range c.Files {
			c.Files[i].Module = moduleOf(c.Files[i].Name, modules)
		}
	}
	if err := writeCommits(dir, commits); err != nil {
		return err
	}
	if _, _, err := refreshDeps(dir, r); err != nil { // the files were just approved for this
		return err
	}
	say(detectLang(os.Getenv), "depsUpdated", r.Name, n)
	return nil
}
