package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseManifest(t *testing.T) {
	for _, tc := range []struct{ file, content, want string }{
		{"package.json", `{"dependencies":{"react":"^18.2.0","local":"file:../x","gh":"user/repo","ws":"workspace:*"},
			"devDependencies":{"typescript":"5.4.0","react":"^18.2.0"},"peerDependencies":{"htmx.org":"^2.0.0"}}`,
			"htmx.org@^2.0.0 react@^18.2.0 typescript@5.4.0 ws@"}, // a local one only by a name that shows a tech
		{"Pipfile", "[[source]]\nurl = \"https://pypi.org/simple\"\n\n[packages]\nrequests = \"*\"\ndjango = {version = \">=4.2\"}\nmylib = {path = \"./mylib\", editable = true}\n\n[dev-packages]\npytest = \"*\"\n\n[requires]\npython_version = \"3.11\"\n",
			"django@>=4.2 pytest@* requests@*"},
		{"go.mod", "module x\n\ngo 1.22\n\nrequire github.com/a/b v1.0.0\n\nrequire (\n\t// pinned until v3\n\tgithub.com/c/d v2.1.0 // see #12\n\tgithub.com/e/f v0.1.0 // indirect\n)\n",
			"github.com/a/b@v1.0.0 github.com/c/d@v2.1.0"},
		// pip-compile's output: hashes on the same line, or on lines continued with a backslash
		{"requirements.txt", "certifi==2024.2.2 \\\n    --hash=sha256:0123\nidna==3.6 --hash=sha256:4567\nsix==1.16\t--hash=sha256:89ab\nurllib3==2.2.1\\\r\n",
			"certifi@2024.2.2 idna@3.6 six@1.16 urllib3@2.2.1"},
		{"requirements.txt", "# pinned\nDjango==4.2\nrequests[socks]>=2.31 ; python_version > '3.8'\n-r base.txt\n./local\ngit+https://x/y.git\nnumpy\n" +
			"-e git+https://github.com/pallets/flask.git#egg=flask\nfastapi @ git+https://github.com/fastapi/fastapi\nmylib @ file:///src/mylib\n",
			"Django@4.2 fastapi@ flask@ numpy@ requests@>=2.31"},
		{"pyproject.toml", "[project]\nname = \"x\"\ndependencies = [\n  \"fastapi>=0.110\",\n  \"uvicorn[standard]\",\n]\n\n[tool.poetry.dependencies]\npython = \"^3.11\"\npydantic = \"^2.0\"\nmylib = { path = \"../mylib\" }\n",
			"fastapi@>=0.110 pydantic@^2.0 uvicorn@"},
		{"pom.xml", `<project xmlns="http://maven.apache.org/POM/4.0.0"><dependencies>
			<dependency><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-web</artifactId></dependency>
			<dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13.2</version></dependency>
			</dependencies></project>`, "junit:junit@4.13.2 org.springframework.boot:spring-boot-starter-web@"},
		{"build.gradle.kts", "dependencies {\n  implementation(\"io.ktor:ktor-server-core:2.3.0\")\n  testImplementation(platform(\"org.junit:junit-bom:5.10.0\"))\n  implementation(project(\":core\"))\n  implementation(libs.okhttp)\n}\n",
			"io.ktor:ktor-server-core@2.3.0 org.junit:junit-bom@5.10.0"},
		{"libs.versions.toml", "[versions]\nokhttp = \"4.12.0\"\n\n[libraries]\nokhttp = { module = \"com.squareup.okhttp3:okhttp\", version.ref = \"okhttp\" }\ngson = \"com.google.code.gson:gson:2.10\"\ncoil = { group = \"io.coil-kt\", name = \"coil\", version = \"2.5.0\" }\n",
			"com.google.code.gson:gson@2.10 com.squareup.okhttp3:okhttp@4.12.0 io.coil-kt:coil@2.5.0"},
		{"Cargo.toml", "[package]\nname = \"x\"\nversion = \"0.1.0\"\n\n[dependencies]\nserde = { version = \"1.0\", features = [\"derive\"] }\ntokio = \"1\"\nlocal = { path = \"../local\" }\naxum = { git = \"https://github.com/tokio-rs/axum\" }\n\n[dependencies.mine]\npath = \"../mine\"\n\n[dependencies.reqwest]\nversion = \"0.12\"\n\n[dev-dependencies]\ninsta = \"1.34\"\n",
			"axum@ insta@1.34 reqwest@0.12 serde@1.0 tokio@1"},
	} {
		var got []string
		for _, d := range parseManifest(tc.file, tc.content) {
			got = append(got, d.name+"@"+d.version)
		}
		if s := strings.Join(got, " "); s != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.file, s, tc.want)
		}
	}
}

func TestParseSelection(t *testing.T) {
	for in, want := range map[string]string{"all": "[1 2 3 4]", "none": "[]", "1,3": "[1 3]", "2-4, 1": "[1 2 3 4]", "3,3": "[3]"} {
		sel, err := parseSelection(in, 4)
		if got := fmt.Sprint(sel); err != nil || got != want {
			t.Errorf("parseSelection(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"0", "5", "3-1", "x", "1-"} {
		if _, err := parseSelection(bad, 4); err == nil {
			t.Errorf("parseSelection(%q) accepted", bad)
		}
	}
}

// TestDepsInMonorepo approves two of three manifest files and checks that only those are read,
// and that each changed file points at the module of its nearest approved manifest.
func TestDepsInMonorepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, data := t.TempDir(), t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	files := map[string]string{
		"package.json":             `{"dependencies":{"react":"18.0.0"}}`,
		"services/api/go.mod":      "module api\n\nrequire github.com/gin-gonic/gin v1.9.0\n",
		"services/api/main.go":     "package main\n",
		"tools/requirements.txt":   "secret-internal-tool==1.0\n",
		"web/Cargo.toml":           "[dependencies]\nundecided = \"1\"\n",
		"web/src/App.jsx":          "export default 1\n",
		"node_modules/x/go.mod":    "module x\n",
		"Example/app/package.json": "{}",
	}
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "me@example.com")
	run("config", "user.name", "me")
	run("add", "-f", ".")
	run("commit", "-q", "-m", "init")

	r := newRepo(repo)
	if got := strings.Join(manifestCandidates(repo), " "); got != "package.json services/api/go.mod tools/requirements.txt web/Cargo.toml" {
		t.Fatalf("candidates = %q (node_modules must be skipped)", got)
	}
	r.Manifests = map[string]bool{"package.json": true, "services/api/go.mod": true, "tools/requirements.txt": false}
	if err := saveConfig(data, Config{Deps: true}); err != nil {
		t.Fatal(err)
	}
	// git itself logs every command gitfolio runs; this is how users can check it too (README).
	// read lists the files `git cat-file`, the only command that reads file contents, ran for.
	read := func(do func() error) string {
		t.Helper()
		trace := filepath.Join(t.TempDir(), "trace")
		t.Setenv("GIT_TRACE", trace)
		if err := do(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_TRACE", "")
		b, _ := os.ReadFile(trace)
		var files []string
		for _, line := range strings.Split(string(b), "\n") {
			if _, args, ok := strings.Cut(line, "built-in: git cat-file "); ok {
				files = append(files, args)
			}
		}
		slices.Sort(files)
		return strings.Join(files, ", ")
	}
	if got := read(func() error { _, err := scanRepo(data, &r, false); return err }); got != "" {
		t.Errorf("scan read %q; collecting commits must not read package manager files", got)
	}
	if got := read(func() error { _, _, err := refreshDeps(data, &r); return err }); got != "-p HEAD:package.json, -p HEAD:services/api/go.mod" {
		t.Errorf("deps scan read %q; only the approved files may be read, never the declined or undecided ones", got)
	}
	// scan and sync (the daily sync is a sync) read them only with deps auto on; push-wait never does.
	if err := saveRepos(data, []Repo{r}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITFOLIO_API_URL", "http://127.0.0.1:9") // not logged in, offline: sync stops after collecting
	approved := "-p HEAD:package.json, -p HEAD:services/api/go.mod"
	for _, auto := range []bool{false, true} {
		if err := saveConfig(data, Config{Deps: true, DepsAuto: auto}); err != nil {
			t.Fatal(err)
		}
		want := map[bool]string{true: approved}[auto]
		for name, do := range map[string]func(){"scan": func() { cmdScan(data, []string{"--all"}) }, "sync": func() { cmdSync(data, nil) }} {
			if got := read(func() error { do(); return nil }); got != want {
				t.Errorf("%s with deps auto %v read %q, want %q", name, auto, got, want)
			}
		}
	}
	if err := saveConfig(data, Config{Deps: true}); err != nil {
		t.Fatal(err)
	}

	deps, err := loadDeps(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range deps {
		names = append(names, d.Ecosystem+":"+d.Name)
	}
	if got := strings.Join(names, " "); got != "npm:react go:github.com/gin-gonic/gin" && got != "go:github.com/gin-gonic/gin npm:react" {
		t.Errorf("deps = %q; the declined requirements.txt must not be read", got)
	}

	commits, err := readCommits(data)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits = %v, %v", commits, err)
	}
	module := map[string]string{}
	for _, f := range commits[0].Files {
		module[f.Name] = f.Module
	}
	if module["services/api/main.go"] != moduleID("services/api") || module["web/src/App.jsx"] != moduleID("") || module["tools/requirements.txt"] != moduleID("") {
		t.Errorf("modules = %v; want services/api/main.go in services/api, the rest in the root module", module)
	}
}

// TestExportOnlyTouchedModules checks that export sends the dependencies of modules the user's commits
// touched, and never the module IDs themselves.
func TestExportOnlyTouchedModules(t *testing.T) {
	data := t.TempDir()
	if err := saveRepos(data, []Repo{{ID: "r1", Name: "mono", Provider: "GITHUB", Namespace: "o/mono"}}); err != nil {
		t.Fatal(err)
	}
	if err := appendCommits(data, []Commit{{Repo: "r1", Hash: "h1", Files: []FileStat{{Name: "main.go", Add: 1, Module: "api"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(filepath.Join(data, "deps.json"), []Dependency{
		{Repo: "r1", Module: "api", Ecosystem: "go", Name: "github.com/gin-gonic/gin"},
		{Repo: "r1", Module: "web", Ecosystem: "npm", Name: "react"},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := buildExport(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Dependencies) != 1 || out.Dependencies[0].Name != "github.com/gin-gonic/gin" || out.Dependencies[0].Namespace != "o/mono" {
		t.Errorf("dependencies = %+v; want only gin from the touched api module", out.Dependencies)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), `"module"`) {
		t.Errorf("export contains module IDs: %s", b)
	}
}

// TestReviewManifestsTogether checks that the files of several repositories are numbered in one list
// and answered with one selection.
func TestReviewManifestsTogether(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	mk := func(files ...string) Repo {
		dir := t.TempDir()
		for _, f := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=me@example.com", "-c", "user.name=me", "commit", "-q", "-m", "init"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return newRepo(dir)
	}
	rs := []Repo{mk("package.json", "api/go.mod", "web/package.json"), mk("README.md"), mk("pom.xml")}
	rs[2].Manifests = map[string]bool{"pom.xml": true}
	defer func(r *bufio.Reader) { stdin = r }(stdin)
	stdin = bufio.NewReader(strings.NewReader("1,4\n")) // 1-3: first repository (sorted), 4: pom.xml

	changed := reviewManifests(rs)
	if !slices.Equal(changed, []int{0}) {
		t.Errorf("changed = %v; only the first repository's decisions changed", changed)
	}
	want := map[string]bool{"api/go.mod": true, "package.json": false, "web/package.json": false}
	if !maps.Equal(rs[0].Manifests, want) || rs[1].Manifests != nil || !rs[2].Manifests["pom.xml"] {
		t.Errorf("manifests = %v, %v, %v", rs[0].Manifests, rs[1].Manifests, rs[2].Manifests)
	}
}

// The ten more package manager files aline.team takes: each gives the names in the form its ecosystem
// sends, and those names give the techs (depTechs) aline.team finds for them.
func TestMoreManifests(t *testing.T) {
	for _, tc := range []struct{ file, content, names, techs string }{
		{"composer.json", `{"require":{"php":"^8.2","ext-json":"*","laravel/framework":"^11.0"},"require-dev":{"phpunit/phpunit":"^10"}}`,
			"composer:laravel/framework@^11.0 composer:phpunit/phpunit@^10", "LARAVEL"},
		{"Gemfile", "source 'https://rubygems.org'\ngem 'rails', '~> 7.1'\ngem 'mine', path: '../mine'\n  gem \"puma\"\ngem 'sinatra', github: 'sinatra/sinatra'\n",
			"rubygems:puma@ rubygems:rails@~> 7.1 rubygems:sinatra@", "RUBY_ON_RAILS SINATRA"},
		{"pubspec.yaml", "name: app\ndependencies:\n  flutter:\n    sdk: flutter\n  http: ^1.1.0\n  mine:\n    path: ../mine\ndev_dependencies:\n  flutter_test:\n    sdk: flutter\nflutter:\n  uses-material-design: true\n",
			"pub:flutter@ pub:flutter_test@ pub:http@^1.1.0", "FLUTTER"},
		{"Package.swift", `dependencies: [
    .package(url: "https://github.com/vapor/vapor.git", from: "4.89.0"),
    .package(url: "https://git.example.internal/team/kit.git", branch: "main"),
]`, "swift:https://github.com/vapor/vapor.git@4.89.0", "VAPOR"},
		{"Podfile", "platform :ios, '15.0'\ntarget 'App' do\n  pod 'Alamofire', '~> 5.8'\n  pod 'Local', :path => '../Local'\n  pod 'SnapKit', :git => 'https://github.com/SnapKit/SnapKit.git'\nend\n",
			"cocoapods:Alamofire@~> 5.8", "IOS_NATIVE"},
		{"mix.exs", "defp deps do\n  [\n    {:phoenix, \"~> 1.7\"},\n    {:jason, \">= 0.0.0\"},\n    {:mine, path: \"../mine\"}\n  ]\nend\n",
			"hex:jason@>= 0.0.0 hex:phoenix@~> 1.7", "PHOENIX"},
		{"App.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"><ItemGroup><PackageReference Include="Serilog" Version="3.1.1" />` +
			`<Reference Include="System.Web, Version=4.0.0.0, Culture=neutral" /></ItemGroup></Project>`,
			"nuget:Serilog@3.1.1 nuget:System.Web@ nuget:sdk:Microsoft.NET.Sdk.Web@", "ASP_NET_CORE"},
		{"deno.json", `{"imports":{"$fresh/":"https://deno.land/x/fresh@1.6.0/","@std/assert":"jsr:@std/assert@^1","secret/":"https://git.example.internal/x/"}}`,
			"deno:$fresh/@ deno:@std/assert@ deno:deno.land/x/fresh@ deno:secret/@", "FRESH"}, // of a value only a module id that shows a tech
		{"build.sbt", `libraryDependencies ++= Seq(
  "com.typesafe.akka" %% "akka-actor-typed" % "2.8.5",
  "org.scalatest" %% "scalatest" % "3.2.17" % Test
)`, "sbt:com.typesafe.akka:akka-actor-typed@2.8.5 sbt:org.scalatest:scalatest@3.2.17", "AKKA"},
		{"environment.yml", "name: ml\nchannels:\n  - conda-forge\ndependencies:\n  - python=3.11\n  - pytorch>=2.1\n  - conda-forge::numpy\n  - pip\n  - pip:\n    - django==4.2\n",
			"pypi:django@4.2 conda:numpy@ conda:pytorch@2.1", "DJANGO NUMPY PYTORCH"}, // conda falls back to PyPI's names
	} {
		eco := manifestEcosystem(tc.file)
		var names []string
		byEco := map[string][]string{}
		for _, d := range parseManifest(tc.file, tc.content) {
			e := cmp.Or(d.eco, eco)
			names = append(names, e+":"+d.name+"@"+d.version)
			byEco[e] = append(byEco[e], d.name)
		}
		if s := strings.Join(names, " "); s != tc.names {
			t.Errorf("%s:\n got  %s\n want %s", tc.file, s, tc.names)
		}
		techs := map[string]bool{}
		for e, n := range byEco {
			maps.Copy(techs, depTechs(e, n))
		}
		if s := strings.Join(slices.Sorted(maps.Keys(techs)), " "); s != tc.techs {
			t.Errorf("%s techs: %s, want %s", tc.file, s, tc.techs)
		}
	}
	files := []string{"app/pubspec.yaml", "app/ios/Podfile", "native/ios/Podfile", "web/package.json", "web/ios/App/Podfile"}
	for p, want := range map[string]bool{"app/ios/Podfile": true, "native/ios/Podfile": false, "web/ios/App/Podfile": true} {
		if crossPlatformIOS(p, files) != want {
			t.Errorf("crossPlatformIOS(%s) = %v", p, !want)
		}
	}
}
