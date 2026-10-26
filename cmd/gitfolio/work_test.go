package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkKind(t *testing.T) {
	for p, want := range map[string]string{
		"cmd/gitfolio/git.go": "source", "cmd/gitfolio/git_test.go": "test", "src/test/java/a/FooTest.java": "test",
		"src/main/java/a/Latest.java": "source", "web/App.spec.ts": "test", "tests/test_api.py": "test", "app/x_spec.rb": "test",
		"README.md": "docs", "docs/guide.html": "docs", "Dockerfile": "ci_deploy", "deploy/Dockerfile.prod": "ci_deploy",
		".github/workflows/ci.yml": "ci_deploy", "infra/main.tf": "ci_deploy", "charts/app/values.yaml": "ci_deploy",
		"pom.xml": "build", "web/package.json": "build", "go.mod": "build", "App.csproj": "build",
		"db/schema.sql": "database", "src/main/resources/db/migration/V1__init.yml": "database",
		"config/app.yml": "config", "src/main/resources/application.properties": "config",
		"go.sum": "", "web/package-lock.json": "", "assets/logo.png": "other",
		// where these kinds differ from aline.team's package files (packageFiles), on purpose
		"setup.py": "source", "Package.swift": "source", "mix.exs": "source", "build.gradle.kts": "build",
		"gradle.properties": "config", "AndroidManifest.xml": "config",
	} {
		if got := workKind(p); got != want {
			t.Errorf("workKind(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestParsePeriod(t *testing.T) {
	p, err := parsePeriod("2025-06", "2025")
	if err != nil {
		t.Fatal(err)
	}
	in := func(s string) bool { d, _ := time.ParseInLocation(time.DateTime, s, time.Local); return p.has(d) }
	if in("2025-05-31 23:59:59") || !in("2025-06-01 00:00:00") || !in("2025-12-31 23:59:59") || in("2026-01-01 00:00:00") {
		t.Errorf("period 2025-06 ~ 2025 = %v ~ %v", p.from, p.to)
	}
	if _, err := parsePeriod("June", ""); err == nil {
		t.Error("a period that is no date was taken")
	}
	if p, _ := parsePeriod("", "2025-06-15"); !p.has(time.Date(2025, 6, 15, 22, 0, 0, 0, time.Local)) || p.has(time.Date(2025, 6, 16, 0, 0, 0, 0, time.Local)) {
		t.Error("the last day is not whole")
	}
}

// work and timeline count only what config ai lets AI tools see, and never name a path or folder.
func TestWorkTools(t *testing.T) {
	ts := httptest.NewServer((&fakeAline{token: testToken}).handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveRepos(dir, []Repo{
		{ID: "a", Name: "app", Provider: "GITHUB", Namespace: "me/app", AI: aiExposure{Branches: true}},
		{ID: "b", Name: "secret", Provider: "GITHUB", Namespace: "corp/secret", AI: aiExposure{Hidden: true}},
	}))
	c := func(repo, date, msg, branch string, ct string, files ...FileStat) Commit {
		return Commit{Repo: repo, Hash: date, Date: date, Message: msg, Branch: branch, CreationType: ct, Files: files}
	}
	must(writeCommits(dir, []Commit{
		c("a", "2025-06-10T12:00:00Z", "feat: billing", "feature/pay", creationHuman,
			FileStat{Name: "billing/pay.go", Add: 60, Created: true}, FileStat{Name: "billing/pay_test.go", Add: 30}, FileStat{Name: "go.sum", Add: 500}),
		c("a", "2025-07-01T12:00:00Z", "fix: rounding", "main", creationHumanCoAI, FileStat{Name: "README.md", Add: 10}),
		c("b", "2025-07-02T12:00:00Z", "feat: secret thing", "main", creationHuman, FileStat{Name: "x/y.go", Add: 1000}),
	}))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"work","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"work","arguments":{"from":"2025-07"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"timeline","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"work","arguments":{"repository":"secret"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"work","arguments":{"to":"July"}}}`,
	}, "\n")
	var res strings.Builder
	must(serveMCP(dir, strings.NewReader(in), &res))
	text, isErr := map[int]string{}, map[int]bool{}
	dec := json.NewDecoder(strings.NewReader(res.String()))
	for dec.More() {
		var r struct {
			ID     int
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
		}
		must(dec.Decode(&r))
		text[r.ID], isErr[r.ID] = r.Result.Content[0].Text, r.Result.IsError
	}
	var work struct {
		Repositories []workSummary
		Total        workSummary
	}
	must(json.Unmarshal([]byte(text[1]), &work))
	if len(work.Repositories) != 1 || work.Total.Commits != 2 {
		t.Fatalf("work = %s", text[1])
	}
	a := work.Repositories[0]
	if a.Repository != "app" || a.Commits != 2 || a.ActiveMonths != 2 || a.CommitTypes["feature"] != 50 || a.CommitTypes["fix"] != 50 ||
		a.WorkKinds["source"] != 60 || a.WorkKinds["test"] != 30 || a.WorkKinds["docs"] != 10 || a.Languages["Go"] != 100 ||
		a.WithAI != 50 || a.FilesCreated != 1 || strings.Join(a.Branches, ",") != "feature/pay,main" {
		t.Errorf("work for app = %+v", a)
	}
	if !strings.Contains(text[2], `"commits":1`) || strings.Contains(text[2], `"feature"`) {
		t.Errorf("work from 2025-07 = %s", text[2])
	}
	if !strings.Contains(text[3], `"month":"2025-06"`) || !strings.Contains(text[3], `"month":"2025-07","commits":1,"repositories":["app"]`) {
		t.Errorf("timeline = %s", text[3])
	}
	want := `{"months":[{"month":"2025-06","commits":1,"repositories":["app"],"languagesPercentOfChangedLines":{"Go":100},"commitTypesPercent":{"feature":100}},` +
		`{"month":"2025-07","commits":1,"repositories":["app"],"commitTypesPercent":{"fix":100}}]}`
	if text[3] != want {
		t.Errorf("timeline = %s\nwant %s", text[3], want)
	}
	if !isErr[4] || !isErr[5] {
		t.Errorf("hidden repository or bad period answered: %s / %s", text[4], text[5])
	}
	for id := 1; id <= 3; id++ {
		for _, leak := range []string{"secret", "billing", "pay.go", "corp/", "rounding"} {
			if strings.Contains(text[id], leak) {
				t.Errorf("tool %d shows %q: %s", id, leak, text[id])
			}
		}
	}
}

// commits answers only where the user lets messages show, subjects or full, without trailers or emails.
func TestCommitsTool(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveRepos(dir, []Repo{
		{ID: "a", Name: "app", AI: aiExposure{Messages: "full"}},
		{ID: "b", Name: "lib", AI: aiExposure{Messages: "subjects", Branches: true}},
		{ID: "c", Name: "work"},
	}))
	msg := "feat: invoices for teams\n\nSplit totals per seat; ask kim@corp.example for the rates.\nThis reverts commit 0123456789abcdef0123456789abcdef01234567.\n\nCo-authored-by: Kim <kim@corp.example>\nSigned-off-by: Me <me@example.com>"
	var commits []Commit
	for i, r := range []string{"a", "a", "a", "b", "c"} {
		commits = append(commits, Commit{Repo: r, Hash: fmt.Sprint("h", i), Date: fmt.Sprintf("2025-06-%02dT12:00:00Z", 10+i),
			Message: map[bool]string{true: msg, false: "fix: rounding"}[i%2 == 0], Branch: "feature/x", CreationType: creationHuman,
			Files: []FileStat{{Name: "billing/invoice.go", Add: 5, Del: 1}, {Name: "billing/invoice_test.go", Add: 2}}})
	}
	must(writeCommits(dir, commits))
	call := func(args map[string]any) (map[string]any, error) {
		v, err := mcpCommits(dir, args)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(v)
		var m map[string]any
		must(json.Unmarshal(b, &m))
		return m, nil
	}
	full, err := call(map[string]any{"repository": "app", "limit": float64(2)})
	must(err)
	b, _ := json.Marshal(full)
	s := string(b)
	if full["total"] != float64(3) || full["nextOffset"] != float64(2) || !strings.Contains(s, `"date":"2025-06-12"`) || !strings.Contains(s, `"type":"feature","workKinds":["source","test"]`) || !strings.Contains(s, `"linesAdded":7`) ||
		!strings.Contains(s, `"body":"Split totals per seat; ask [email] for the rates.\nThis reverts commit [hash]."`) || strings.Contains(s, "Co-authored") ||
		strings.Contains(s, "Signed-off") || strings.Contains(s, "@") || strings.Contains(s, "h0") || strings.Contains(s, "billing") || strings.Contains(s, "feature/x") {
		t.Errorf("full messages = %s", s)
	}
	if fix, _ := call(map[string]any{"repository": "app", "type": "fix"}); fix["total"] != float64(1) {
		t.Errorf("fixes only = %v", fix)
	}
	sub, err := call(map[string]any{"repository": "lib"})
	must(err)
	b, _ = json.Marshal(sub)
	if s := string(b); !strings.Contains(s, `"subject":"fix: rounding"`) || strings.Contains(s, `"body"`) || !strings.Contains(s, `"branch":"feature/x"`) {
		t.Errorf("subjects only = %s", s)
	}
	if _, err := call(map[string]any{"repository": "work"}); err == nil || !strings.Contains(err.Error(), "config ai messages") {
		t.Errorf("messages off: %v", err)
	}
	if _, err := call(map[string]any{}); err == nil {
		t.Error("no repository named, and it answered")
	}
}
