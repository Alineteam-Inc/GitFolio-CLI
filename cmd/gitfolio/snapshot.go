package main

import (
	"cmp"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
)

// The statistics snapshot is what aline.team gets of a repository instead of its commit records: the
// user's pushed history folded into counts per UTC day, from which it works out the developer type and
// how long each part of the tech stack was used. No hash, message, file path or branch is in it.
// The classification rules below are aline.team's, copied so that the counts are the ones it would get
// from the commit records.

// snapshotFormat is the version of the snapshot's fields. Adding one changes every snapshot, so they are
// all sent again after the update.
const snapshotFormat = 1

// pair counts commits without an AI signal (creation type HUMAN) and with one, in that order.
type pair [2]int

func aiIndex(creationType string) int {
	if creationType == creationHuman {
		return 0
	}
	return 1
}

type snapshot struct {
	Format      int                 `json:"format"`
	Provider    string              `json:"provider"`
	Namespace   string              `json:"namespace"`
	AuthorEmail string              `json:"authorEmail"`
	AsOf        string              `json:"asOf"`                 // the newest pushed commit read in the repository
	Days        map[string]*statDay `json:"days"`                 // UTC date → counts
	Markers     []string            `json:"markers,omitempty"`    // files whose presence shows a tool (addMarkers)
	StackLangs  []string            `json:"stackLangs,omitempty"` // languages of any file the user changed
}

// statDay is one UTC day of a snapshot. System commits (merges, releases, ...) count nowhere but in the
// markers, and code means files of type code or markup (sourceFile).
type statDay struct {
	Commits    pair            `json:"commits,omitzero"`    // the user's commits, one per author time
	Dup        pair            `json:"dup,omitzero"`        // of those, also in a repository sent before this one
	Langs      map[string]pair `json:"langs,omitempty"`     // code file changes per language
	Created    pair            `json:"created,omitzero"`    // code files the user created, each on the first day it was (first)
	EarlyChurn pair            `json:"earlyChurn,omitzero"` // files the user created that were changed again 1-14 days later, each on the first such day
	Touches    pair            `json:"touches,omitzero"`    // changes to code files the user created, a day or more later, by anyone
	Corrective pair            `json:"corrective,omitzero"` // of those, fixes and reverts
	Mine       pair            `json:"mine,omitzero"`       // substantive commits that changed code
	Others     pair            `json:"others,omitzero"`     // of those, the ones that changed a file they did not create
}

// churn is what a scan keeps of the history of others for one day: the later changes to files the user
// created, counted by whether the commit that created the file had an AI signal (churnOf).
type churn struct {
	Touches    pair `json:"t,omitzero"`
	Corrective pair `json:"c,omitzero"`
	Early      pair `json:"e,omitzero"`
}

// origin is when a file the user created was made, and whether that commit had an AI signal.
type origin struct {
	at time.Time
	ai int
}

// first keeps the day a file counts on when it happens to the file again: the first time, but one
// without an AI signal wins, since aline.team counts a file without AI when any of its rows had none.
// aline.team's period ends before this week and starts months ago, so the first time is rarely outside
// it while another is inside.
func first(was, now origin) origin {
	if was.at.IsZero() || now.ai < was.ai || (now.ai == was.ai && now.at.Before(was.at)) {
		return now
	}
	return was
}

// churnOf counts, per UTC day, the changes by anyone but bots to code files the user created, made a day
// or more after the file was created, and returns the newest commit time read. System commits do not
// count. Files are followed by path, as in firstChanges. commits is in git log order, newest first.
// A file changed again within 14 days counts once, on the first such day (first).
func churnOf(commits []Commit, mine map[string]bool) (map[string]churn, string) {
	created, early, days := map[string]*origin{}, map[string]origin{}, map[string]churn{}
	var newest time.Time
	for _, c := range slices.Backward(commits) {
		at, err := time.Parse(time.RFC3339, c.Date)
		if err != nil {
			continue
		}
		if at.After(newest) {
			newest = at
		}
		ct, typ := creationType(c, mine), commitType(c.Message)
		touch := typ != "SYSTEM" && !botAuthor(c.AuthorEmail)
		for _, f := range c.Files {
			if !sourceFile(f.Name) {
				continue
			}
			if f.Created {
				delete(created, f.Name) // someone else's file from now on, unless it is the user's
				if ct != "" {
					o := origin{at, aiIndex(ct)}
					created[f.Name] = &o
				}
				continue
			}
			o := created[f.Name]
			if o == nil || !touch {
				continue
			}
			age := int(at.Sub(o.at) / (24 * time.Hour)) // whole days passed, as aline.team counts them
			if age < 1 {
				continue
			}
			k := at.UTC().Format(time.DateOnly)
			d := days[k]
			d.Touches[o.ai]++
			if typ == "FIX" || typ == "HOTFIX" || typ == "REVERT" {
				d.Corrective[o.ai]++
			}
			if age <= 14 {
				early[f.Name] = first(early[f.Name], origin{at, o.ai})
			}
			days[k] = d
		}
	}
	for _, e := range early {
		k := e.at.UTC().Format(time.DateOnly)
		d := days[k]
		d.Early[e.ai]++
		days[k] = d
	}
	if newest.IsZero() {
		return days, ""
	}
	return days, newest.UTC().Format(time.RFC3339)
}

// substantive are the commit types that count as the user's contribution.
var substantive = map[string]bool{"ADD": true, "FIX": true, "HOTFIX": true, "PERF": true, "REFACTOR": true, "TEST": true, "DOCUMENTATION": true}

// snapshots builds the snapshot of each repository with a git service remote from the user's stored
// commits and what the last scan counted of others (Repo.Churn). Repositories go by ID, and a commit
// that is also in one before by its author time counts as dup, so the account's commits count once.
func snapshots(repos []Repo, commits []Commit, author string) []snapshot {
	byRepo := commitsByRepo(commits)
	repos = slices.SortedFunc(slices.Values(repos), func(a, b Repo) int { return cmp.Compare(a.ID, b.ID) })
	owner := map[int64]bool{} // author times of commits counted in a repository before
	out := []snapshot{}
	for _, r := range repos {
		if r.Namespace == "" {
			continue // never sent
		}
		s := snapshot{Format: snapshotFormat, Provider: r.Provider, Namespace: r.Namespace, AuthorEmail: author, AsOf: r.AsOf, Days: map[string]*statDay{}}
		day := func(k string) *statDay {
			if s.Days[k] == nil {
				s.Days[k] = &statDay{}
			}
			return s.Days[k]
		}
		for k, ch := range r.Churn {
			d := day(k)
			d.Touches, d.Corrective, d.EarlyChurn = ch.Touches, ch.Corrective, ch.Early
		}
		times, created := map[int64]bool{}, map[string]origin{} // created: each path's last creation
		marks, langs := map[string]bool{}, map[string]bool{}
		for _, c := range byRepo[r.ID] {
			at, err := time.Parse(time.RFC3339, c.Date)
			if err != nil || c.Branch == "" { // not pushed yet (read from HEAD before the first push)
				continue
			}
			for _, f := range c.Files {
				addMarkers(marks, f.Name)
			}
			typ := commitType(c.Message)
			if typ == "SYSTEM" {
				continue
			}
			ai, k := aiIndex(c.CreationType), at.UTC().Format(time.DateOnly)
			d := day(k)
			if t := at.Unix(); !times[t] {
				times[t] = true
				d.Commits[ai]++
				if owner[t] {
					d.Dup[ai]++
				}
			}
			var code, existing bool
			for _, f := range c.Files {
				lang := languageOf(f.Name)
				if lang != "" {
					langs[lang] = true
				}
				if !sourceFile(f.Name) {
					continue
				}
				code = true
				if lang != "" {
					if d.Langs == nil {
						d.Langs = map[string]pair{}
					}
					p := d.Langs[lang]
					p[ai]++
					d.Langs[lang] = p
				}
				if !f.Created {
					existing = true
				} else {
					created[f.Name] = first(created[f.Name], origin{at, ai})
				}
			}
			if code && substantive[typ] {
				d.Mine[ai]++
				if existing {
					d.Others[ai]++
				}
			}
			s.AsOf = max(s.AsOf, at.UTC().Format(time.RFC3339)) // when no scan set it yet
		}
		for _, o := range created { // once per path, as aline.team counts the files created in a period
			day(o.at.UTC().Format(time.DateOnly)).Created[o.ai]++
		}
		maps.Copy(owner, times)
		s.Markers, s.StackLangs = slices.Sorted(maps.Keys(marks)), slices.Sorted(maps.Keys(langs))
		out = append(out, s)
	}
	return out
}

// File types, aline.team's way, by file name: build and package files by name first (one starting with
// Dockerfile or Makefile too, unless its extension is code's, prose's or media's: DockerfileParser.java,
// makefile.png), then the extension. Only code and markup count in a snapshot.
var (
	packageFiles = set("pom.xml build.gradle build.gradle.kts settings.gradle settings.gradle.kts gradle.properties package.json " +
		"package-lock.json yarn.lock pnpm-lock.yaml bun.lockb deno.json deno.lock requirements.txt pyproject.toml Pipfile Pipfile.lock " +
		"setup.py setup.cfg tox.ini environment.yml Gemfile Gemfile.lock Rakefile rakefile composer.json composer.lock go.mod go.sum " +
		"Cargo.toml Cargo.lock global.json CMakeLists.txt Makefile Package.swift Podfile Podfile.lock pubspec.yaml pubspec.lock " +
		"AndroidManifest.xml build.sbt cabal.project cabal.project.freeze package.yaml stack.yaml stack.yaml.lock mix.exs mix.lock " +
		"rebar.config rebar.lock Project.toml Manifest.toml Makefile.PL cpanfile DESCRIPTION flake.nix flake.lock nx.json turbo.json " +
		"lerna.json Chart.yaml helmfile.yaml .pre-commit-config.yaml lefthook.yml mise.toml .tool-versions BUILD WORKSPACE")
	codeExtensions = set("c cpp cc cxx h hpp asm s java class kt kts scala groovy gvy js mjs cjs ts tsx jsx py pyx pxd go rs rb php " +
		"swift m mm cs vb fs fsi fsx sh bash zsh fish ps1 psm1 psd1 hs lhs ml mli clj cljs cljc ex exs erl hrl d nim cr jl sol pl lua r dart")
	markupExtensions = set("html htm xhtml css scss sass less vue svelte astro pug haml erb ejs hbs handlebars jinja j2 njk twig liquid")
)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// sourceFile reports whether the file at repository path p is code or markup.
func sourceFile(p string) bool {
	name, e := path.Base(strings.ReplaceAll(p, `\`, "/")), extension(p)
	if lower := strings.ToLower(name); packageFiles[name] || (strings.HasPrefix(lower, "dockerfile") || strings.HasPrefix(lower, "makefile")) && !prefixBlockingExts[e] {
		return false
	}
	return codeExtensions[e] || markupExtensions[e]
}

// extension is the lowercased extension of the file name, "" without one (".gitignore" has none).
func extension(p string) string {
	name := path.Base(strings.ReplaceAll(p, `\`, "/"))
	if i := strings.LastIndex(name, "."); i > 0 {
		return strings.ToLower(strings.TrimSpace(name[i+1:]))
	}
	return ""
}

// botAuthor reports whether a commit's author is a bot, whose changes do not count against the user's files.
func botAuthor(email string) bool {
	e := strings.ToLower(email)
	return strings.Contains(e, "[bot]") || strings.Contains(e, "-robot@") || strings.Contains(e, "dependabot") || strings.Contains(e, "renovate")
}

// presenceFiles are the file names whose presence alone shows a tool (Helm, Bun, Nx, CMake, ...).
var presenceFiles = set("chart.yaml helmfile.yaml bun.lockb nx.json turbo.json cmakelists.txt androidmanifest.xml")

// addMarkers adds what the file at path p shows by its presence: its name, "dockerfile" or "*.tf" (not in
// a folder of other people's code or samples, skipDirs, or under an android/ or ios/ folder, which
// Flutter and React Native make), and "*.vue" or "*.svelte".
func addMarkers(m map[string]bool, p string) {
	dirs, base := path.Split(strings.ToLower(strings.ReplaceAll(p, `\`, "/")))
	if !inSkipDir(p) && !slices.ContainsFunc(strings.Split(dirs, "/"), func(s string) bool { return s == "android" || s == "ios" }) {
		switch {
		case presenceFiles[base]:
			m[base] = true
		case strings.HasPrefix(base, "dockerfile") && !prefixBlockingExts[extension(p)]: // not dockerfile.md
			m["dockerfile"] = true
		case strings.HasSuffix(base, ".tf"):
			m["*.tf"] = true
		}
	}
	if e := extension(p); e == "vue" || e == "svelte" {
		m["*."+e] = true
	}
}
