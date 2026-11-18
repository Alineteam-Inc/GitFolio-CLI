package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitfolio stats gets the developer type from aline.team the first time, keeps it, and shows the kept
// one after that: again only with --refresh, for another account, or when nothing was there to keep.
func TestStats(t *testing.T) {
	f := &fakeAline{token: testToken, devType: map[string]any{
		"devTypeTitle": "ARCHITECT", "devTypeDescription": "Designs systems", "devTypeComputedAt": "2026-10-05T09:00:00",
		"agilityStat": 72, "agilityStatAi": 65, "stabilityStat": 80, "devTypeTitleAi": "PIONEER", "dominantPosition": "BACKEND",
		"userEmail": "dev@example.com", "sharedCode": "shareXYZ", "devTypeReason": "because",
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	srv := ts.URL
	t.Setenv("GITFOLIO_API_URL", srv)
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut = o, e }(out, errOut)
	out, errOut = &buf, &buf
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: srv}))
	must(saveConfig(dir, Config{})) // automatic sync: the saved type is used without asking again

	must(cmdStats(dir, nil))
	if f.devTypeCalls != 1 || !strings.Contains(buf.String(), "ARCHITECT") || !strings.Contains(buf.String(), "72") ||
		!strings.Contains(buf.String(), "AI 65") || !strings.Contains(buf.String(), "PIONEER") {
		t.Fatalf("first run: %d calls, shown:\n%s", f.devTypeCalls, buf.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, devTypeFile)); !strings.Contains(string(b), "ARCHITECT") ||
		strings.Contains(string(b), "shareXYZ") || strings.Contains(string(b), "userEmail") || strings.Contains(string(b), "because") {
		t.Errorf("kept more than is shown:\n%s", b)
	}
	buf.Reset()
	must(cmdStats(dir, nil)) // kept: no request
	if f.devTypeCalls != 1 || !strings.Contains(buf.String(), "ARCHITECT") || !strings.Contains(buf.String(), "--refresh") {
		t.Errorf("second run: %d calls, shown:\n%s", f.devTypeCalls, buf.String())
	}
	must(cmdStats(dir, []string{"--refresh"}))
	if f.devTypeCalls != 2 {
		t.Errorf("--refresh made %d calls in all, want 2", f.devTypeCalls)
	}
	if cmdStats(dir, []string{"--all"}) == nil {
		t.Error("an unknown flag was taken")
	}

	// aline.team is down: the kept one is shown, with a warning.
	f.down = true
	buf.Reset()
	must(cmdStats(dir, []string{"--refresh"}))
	if !strings.Contains(buf.String(), "ARCHITECT") || !strings.Contains(buf.String(), "Could not get it") {
		t.Errorf("offline refresh shows:\n%s", buf.String())
	}
	f.down = false

	// Another account on this computer: its own type is fetched.
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "other@example.com", Server: srv}))
	must(cmdStats(dir, nil))
	if f.devTypeCalls != 3 || f.devTypeLang != "en" { // the refused refresh never reached it
		t.Errorf("another account made %d calls in all, want 3, asked in %q", f.devTypeCalls, f.devTypeLang)
	}

	// Another screen language: aline.team's texts are fetched again, in it.
	t.Setenv("GITFOLIO_LANG", "ja")
	must(cmdStats(dir, nil))
	t.Setenv("GITFOLIO_LANG", "en")
	if f.devTypeCalls != 4 || f.devTypeLang != "ja" {
		t.Errorf("another language made %d calls in all, want 4, asked in %q", f.devTypeCalls, f.devTypeLang)
	}

	// Nothing to analyse yet: said so, and nothing is kept, so the next run asks again.
	dir = t.TempDir()
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: srv}))
	must(saveConfig(dir, Config{})) // automatic sync: the saved type is used without asking again
	f.devType = nil
	buf.Reset()
	must(cmdStats(dir, nil))
	must(cmdStats(dir, nil))
	if f.devTypeCalls != 6 || !strings.Contains(buf.String(), "nothing to make a developer type from") {
		t.Errorf("no data: %d calls in all, shown:\n%s", f.devTypeCalls, buf.String())
	}
	// Made too slowly, nothing kept: aline.team finishes it, so try again shortly.
	f.devTypeWait = 200 * time.Millisecond
	timeout := apiTimeout
	defer func() { apiTimeout = timeout }()
	apiTimeout = 50 * time.Millisecond
	if err := cmdStats(dir, nil); err == nil || !strings.Contains(err.Error(), "still making") {
		t.Errorf("timeout: %v", err)
	}

	// Not logged in, nothing kept: log in first.
	if err := cmdStats(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("logged out: %v", err)
	}

	// Local mode: the statistics go first, without commit records, and the type is asked for every time.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.mu.Lock() // after the slow answer above, which still holds the fake server while it sleeps
	f.devTypeWait, f.devType, apiTimeout = 0, map[string]any{"devTypeTitle": "BUILDER"}, timeout
	f.mu.Unlock()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/me/app.git"},
		{"-c", "user.email=dev@example.com", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "feat: start"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		if b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	dir = t.TempDir()
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: srv}))
	must(saveConfig(dir, Config{Emails: []string{"dev@example.com"}, AutoSyncOff: true}))
	must(saveRepos(dir, []Repo{newRepo(repo)}))
	f.mu.Lock()
	calls := f.devTypeCalls
	f.mu.Unlock()
	must(cmdStats(dir, nil))
	must(cmdStats(dir, nil))
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.stats["GITHUB/me/app"]; !ok || len(f.commits) != 0 || f.devTypeCalls != calls+2 {
		t.Errorf("local mode: snapshot sent %v, %d commits sent, %d type requests; want the snapshot only and 2 requests",
			ok, len(f.commits), f.devTypeCalls-calls)
	}
}

// Each developer type has its picture, 11 squares wide in known colors, drawn only in color.
func TestDevTypeArt(t *testing.T) {
	for _, title := range []string{"BUILDER", "EXPLORER", "FIXER", "KEEPER", "LEADER", "SPRINTER"} {
		art := devTypeArt[title]
		if len(art) == 0 {
			t.Errorf("%s has no picture", title)
		}
		for _, row := range art {
			bad := strings.IndexFunc(row, func(c rune) bool { _, ok := devTypeColors[c]; return !ok && c != '.' })
			if len(row) != 11 || bad >= 0 {
				t.Errorf("%s row %q", title, row)
			}
		}
	}
	var buf bytes.Buffer
	defer func(o io.Writer, c bool) { out, useColor = o, c }(out, useColor)
	out = &buf
	useColor = false
	drawDevType("EXPLORER")
	if buf.Len() != 0 {
		t.Errorf("drawn without color:\n%s", buf.String())
	}
	useColor = true
	drawDevType("EXPLORER")
	// two squares a line: blue over yellow where the window starts
	if n := strings.Count(buf.String(), "\n"); n != (len(devTypeArt["EXPLORER"])+1)/2 || !strings.Contains(buf.String(), "\x1b[38;5;68;48;5;187m▀") {
		t.Errorf("EXPLORER drawn in %d lines:\n%s", n, buf.String())
	}
	n := buf.Len()
	if drawDevType("UNKNOWN"); buf.Len() != n { // a type this version does not know
		t.Error("an unknown type was drawn")
	}
}

// stack works the tech stack out on this computer, with no login and nothing sent, as aline.team does per
// repository: languages by file, techs a file's presence or the approved dependencies show, each counted
// once per repository with the span and months of the user's commits there.
func TestStack(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = &buf, &buf
	dir := t.TempDir() // no login: none is needed
	commit := func(repo, date, message string, files ...string) Commit {
		c := Commit{Repo: repo, Hash: date, Date: date, Message: message}
		for _, f := range files {
			c.Files = append(c.Files, FileStat{Name: f, Add: 1})
		}
		return c
	}
	api, web := Repo{ID: "api", Name: "api"}, Repo{ID: "web", Name: "web"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveRepos(dir, []Repo{api, web}))
	must(writeCommits(dir, []Commit{
		commit("api", "2024-01-15T10:00:00Z", "feat: start", "src/App.java", "Dockerfile"),
		commit("api", "2024-03-20T09:00:00+09:00", "fix: service", "src/Svc.java"),   // March 20 in UTC too
		commit("api", "2024-09-01T00:00:00Z", "Merge branch 'main'", "src/App.java"), // a merge: not in the span
		commit("api", "2024-10-01T00:00:00Z", "docs: license", "LICENSE"),            // a kind of file unknown: neither
		commit("web", "2024-02-01T00:00:00Z", "feat: app", "app.ts", "Dockerfile.prod"),
		commit("web", "2024-06-01T23:30:00-01:00", "feat: logo", "app.ts", "logo.png", "android/app/Main.vue"), // June 2 in UTC
	}))
	must(saveJSON(filepath.Join(dir, "deps.json"), []Dependency{
		{Repo: "api", Ecosystem: "maven", Name: "org.springframework.boot:spring-boot-starter-data-jpa"},
		{Repo: "api", Ecosystem: "maven", Name: "org.springframework:spring-core"}, // under Spring Boot: not its own
		{Repo: "web", Ecosystem: "npm", Name: "react"},
		{Repo: "web", Ecosystem: "npm", Name: "@reduxjs/toolkit"},
	}))
	v, err := jsonStdout(t, func() error { return cmdStack(dir, []string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	names := map[string][]string{}
	for _, list := range []string{"languages", "frameworks", "libraries"} {
		for _, it := range v[list].([]any) {
			m := it.(map[string]any)
			byName[m["name"].(string)] = m
			names[list] = append(names[list], m["name"].(string))
		}
	}
	if fmt.Sprint(names) != "map[frameworks:[Spring Boot Vue.js] languages:[Java TypeScript] libraries:[Docker JPA React Redux Spring Data JPA]]" {
		t.Errorf("stack: %v", names)
	}
	if d := byName["Docker"]; d["projectCount"] != 2.0 || d["startDate"] != "2024-01-15T00:00:00" || d["endDate"] != "2024-06-02T00:00:00" ||
		d["careerMonths"] != 4.0 || fmt.Sprint(d["activityMonths"]) != "[2024-01 2024-02 2024-03 2024-06]" {
		t.Errorf("Docker, in both repositories: %v", d) // Jan 15 to Jun 2 merged: 4 whole months
	}
	if j := byName["Java"]; j["projectCount"] != 1.0 || j["careerMonths"] != 2.0 || fmt.Sprint(j["activityMonths"]) != "[2024-01 2024-03]" {
		t.Errorf("Java: %v", j) // Jan 15 to Mar 20; the merge and the license commit left out
	}
	jsonOut, out = false, &buf // one command per process otherwise
	if err := cmdStack(dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KIND", "language", "Java", "2024-01", "2024-03", "framework", "Spring Boot", "library", "Redux"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("stack shows no %q:\n%s", want, buf.String())
		}
	}
	buf.Reset()
	if err := cmdStack(t.TempDir(), nil); err != nil || !strings.Contains(buf.String(), "No tech stack yet") {
		t.Errorf("empty stack: %v\n%s", err, buf.String())
	}
	for _, args := range [][]string{{"--all"}, {"--json", "--all"}} {
		if cmdStack(dir, args) == nil {
			t.Errorf("stack %v was taken", args)
		}
	}
}

// Whole months as java.time counts them, over spans merged where they overlap or touch.
func TestMergedMonths(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, c := range []struct {
		spans [][2]string
		want  int
	}{
		{[][2]string{{"2024-01-15T10:00:00Z", "2024-03-15T09:00:00Z"}}, 1}, // the time of day not reached yet
		{[][2]string{{"2024-01-15T10:00:00Z", "2024-03-15T10:00:00Z"}}, 2},
		{[][2]string{{"2024-01-31T00:00:00Z", "2024-02-29T00:00:00Z"}}, 0},
		{[][2]string{{"2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z"}}, 0},
		{[][2]string{{"2024-01-01T00:00:00Z", "2024-03-01T00:00:00Z"}, {"2024-02-01T00:00:00Z", "2024-05-01T00:00:00Z"},
			{"2024-07-01T00:00:00Z", "2024-08-01T00:00:00Z"}}, 5},
		{[][2]string{{"2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"}, {"2024-01-01T00:00:00Z", "2024-02-01T00:00:00Z"}}, 2}, // touching
	} {
		var spans [][2]time.Time
		for _, s := range c.spans {
			spans = append(spans, [2]time.Time{at(s[0]), at(s[1])})
		}
		if got := mergedMonths(spans); got != c.want {
			t.Errorf("%v: %d, want %d", c.spans, got, c.want)
		}
	}
}

// stats and stack --json print one JSON document on stdout for programs, and what people read goes
// to stderr. A failure is printed by main, with the code errorCode gives.
func TestStatsJSON(t *testing.T) {
	f := &fakeAline{token: testToken, devType: map[string]any{"devTypeTitle": "ARCHITECT", "agilityStat": 72, "devTypeReason": "because"}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	var buf, errBuf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = &buf, &errBuf
	dir := t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(dir, Config{}); err != nil { // automatic sync: the type is saved, not asked for each time
		t.Fatal(err)
	}
	stdout := func(cmd func(string, []string) error, args ...string) (map[string]any, error) {
		t.Helper()
		return jsonStdout(t, func() error { return cmd(dir, args) })
	}

	v, err := stdout(cmdStats, "--json")
	typ, _ := v["type"].(map[string]any)
	if err != nil || typ["devTypeTitle"] != "ARCHITECT" || typ["agilityStat"] != 72.0 || v["saved"] != false ||
		v["year"] != float64(time.Now().Year()) || typ["devTypeReason"] != nil || out != errOut { // people's text: on stderr
		t.Errorf("stats --json: %v %v", v, err)
	}
	if v, err := stdout(cmdStats, "--json"); err != nil || v["saved"] != true {
		t.Errorf("stats --json, saved: %v %v", v, err)
	}
	f.devType = nil // nothing to analyse yet: no type, and no failure either
	dir = t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}); err != nil {
		t.Fatal(err)
	}
	if v, err := stdout(cmdStats, "--refresh", "--json"); err != nil || v == nil || v["type"] != nil {
		t.Errorf("stats --json, nothing yet: %v %v", v, err)
	}

	for err, want := range map[error]string{
		failure("loginFirst"): "loginFirst",
		fmt.Errorf("%w again", &apiError{Status: 401, Code: codeTokenInvalid}): "loginFirst",
		&apiError{Status: 500, Code: "S001"}:                                   "failed",
		errors.New("git: no such repository"):                                  "failed",
	} {
		if got := errorCode(err); got != want {
			t.Errorf("errorCode(%v) = %q, want %q", err, got, want)
		}
	}
}

// In local mode stats and stack send the statistics first; with --json only their own result goes to
// stdout, not the sync's as a second document.
func TestLocalModeJSON(t *testing.T) {
	f := &fakeAline{token: testToken, devType: map[string]any{"devTypeTitle": "BUILDER"}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = &buf, &buf
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/me/app.git"},
		{"-c", "user.email=dev@example.com", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "feat: start"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		if b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	dir := t.TempDir()
	for _, err := range []error{saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}),
		saveConfig(dir, Config{Emails: []string{"dev@example.com"}, AutoSyncOff: true}), saveRepos(dir, []Repo{newRepo(repo)})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range []func(string, []string) error{cmdStats, cmdStack} {
		v, err := jsonStdout(t, func() error { return cmd(dir, []string{"--json"}) }) // one document, or it fails
		if err != nil || v == nil {
			t.Errorf("local mode --json: %v %v", v, err)
		}
	}
	if _, ok := f.stats["GITHUB/me/app"]; !ok {
		t.Error("the statistics were not sent first")
	}
}
