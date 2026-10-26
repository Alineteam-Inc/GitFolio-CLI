package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A mistyped command names the ones the user may have meant.
func TestSimilarCommands(t *testing.T) {
	for in, want := range map[string][]string{
		"-v":        {"version"},
		"--version": {"version"},
		"sta":       {"status", "stats", "stack"},
		"st":        {"status", "stats", "stack"},
		"stat":      {"status", "stats", "stack"}, // stack: one letter off
		"stauts":    {"status", "stats"},          // one swap from status, one letter from stats
		"lsit":      {"list"},
		"lgoin":     {"login"},
		"verison":   {"version"},
		"sinc":      {"sync"},
		"logot":     {"logout", "login"},
		"hook":      nil, // for git hooks only, never offered
		"xyz":       nil,
		"portfolo":  {"portfolio"},
		"protfolio": {"portfolio"},
		"port":      {"portfolio", "export"},
	} {
		if got := similarCommands(in); !slices.Equal(got, want) {
			t.Errorf("similarCommands(%q) = %v, want %v", in, got, want)
		}
	}
	if err := run([]string{"stauts"}); err == nil {
		t.Error("a mistyped command ran")
	}
}

// A mistyped word or flag after a command names the ones meant too, and nothing runs.
func TestWrongWords(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	for _, c := range []struct {
		cmd  func(string, []string) error
		args []string
		want string
	}{
		{cmdConfig, []string{"autosyn", "off"}, "Did you mean `gitfolio config autosync`?"},
		{cmdConfig, []string{"autosync", "of"}, "`gitfolio config autosync off`"},
		{cmdConfig, []string{"mask", "ad", "acme"}, "`gitfolio config mask add`"},
		{cmdConfig, []string{"autosync"}, "Usage: gitfolio config"},
		{cmdConfig, []string{"xyz"}, "Usage: gitfolio config"},
		{cmdDeps, []string{"scna"}, "`gitfolio deps scan`"},
		{cmdDeps, []string{"auto", "of"}, "`gitfolio deps auto off`"},
		{cmdEmail, []string{"ad", "a@example.com"}, "`gitfolio email add`"},
		{cmdSchedule, []string{"of"}, "`gitfolio schedule off`"},
		{cmdSchedule, []string{"25:00"}, "Usage: gitfolio schedule"},
		{cmdStats, []string{"--refrsh"}, "`gitfolio stats --refresh`"},
		{cmdSync, []string{"--dryrun"}, "`gitfolio sync` takes no \"--dryrun\". Did you mean `gitfolio sync --dry-run`?"},
		{cmdSync, []string{"-h"}, "Usage: gitfolio sync [--dry-run]"},
		{cmdRemove, []string{"--prge"}, "`gitfolio remove --purge`"},
		{cmdScan, []string{"--al"}, "`gitfolio scan --all`"},
		{cmdHistory, []string{"--al"}, "`gitfolio history --all`"},
		{cmdPortfolio, []string{"--form", "cv.md"}, "`gitfolio portfolio --from`"},
		{cmdPortfolio, []string{"--from"}, "Usage: gitfolio portfolio"},
	} {
		if err := c.cmd(dir, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}

// export, config and sync --dry-run print JSON two-space indented, with "<a@b.com>" kept readable.
func TestPrintJSON(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(o *os.File) { os.Stdout = o }(os.Stdout)
	os.Stdout = w
	err = printJSON(map[string]any{"author": "Dev <dev@example.com>"})
	w.Close()
	got, _ := io.ReadAll(r)
	if err != nil || string(got) != "{\n  \"author\": \"Dev <dev@example.com>\"\n}\n" {
		t.Errorf("printJSON wrote %q (%v)", got, err)
	}
}

// jsonStdout runs fn and returns the one JSON document it printed on stdout (nil for none).
func jsonStdout(t *testing.T, fn func() error) (map[string]any, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(o *os.File) { os.Stdout = o }(os.Stdout)
	os.Stdout = w
	err = fn()
	w.Close()
	b, _ := io.ReadAll(r)
	var v map[string]any
	if len(b) > 0 {
		if jerr := json.Unmarshal(b, &v); jerr != nil {
			t.Fatalf("not one JSON document: %v\n%s", jerr, b)
		}
	}
	return v, err
}

// uninstall unregisters every repository, puts back the hooks that were there before, revokes the
// login when aline.team answers and deletes it either way, keeps the data unless --purge-local, asks
// nothing, and can run again.
func TestUninstall(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &fakeAline{token: testToken}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	var buf bytes.Buffer
	defer func(o, e io.Writer, d time.Duration) { out, errOut, jsonOut, apiTimeout = o, e, false, d }(out, errOut, apiTimeout)
	out, errOut = &buf, &buf

	repo := t.TempDir()
	if b, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	hooks := filepath.Join(repo, ".git", "hooks")
	before := "#!/bin/sh\necho before\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(before), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installHooks(repo); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted") // a registered folder deleted since
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveRepos(dir, []Repo{newRepo(repo), newRepo(gone)}))
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}))

	v, err := jsonStdout(t, func() error { return cmdUninstall(dir, []string{"--json"}) })
	list, _ := v["repositories"].([]any)
	if err != nil || len(list) != 2 || v["revoked"] != true || v["dataDeleted"] != false || f.token != "" {
		t.Fatalf("uninstall: %v %v (server token %q)", v, err, f.token)
	}
	if b, _ := os.ReadFile(filepath.Join(hooks, "pre-push")); string(b) != before {
		t.Errorf("pre-push not put back:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(hooks, "post-commit")); err == nil {
		t.Error("gitfolio's post-commit hook is still there")
	}
	repos, _ := loadRepos(dir)
	creds, _ := loadCredentials(dir)
	if len(repos) != 0 || creds.Token != "" {
		t.Errorf("after uninstall: %d repositories, token kept %v", len(repos), creds.Token != "")
	}

	// Again, offline and logged in once more: nothing to undo, the login goes all the same.
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}))
	ts.Close()
	v, err = jsonStdout(t, func() error { return cmdUninstall(dir, []string{"--json"}) })
	if list, _ := v["repositories"].([]any); err != nil || len(list) != 0 || v["revoked"] != false {
		t.Errorf("second uninstall: %v %v", v, err)
	}
	if creds, _ := loadCredentials(dir); creds.Token != "" {
		t.Error("an unrevoked login was kept")
	}

	jsonOut = false // one command per process otherwise
	must(cmdUninstall(dir, []string{"--purge-local"}))
	if _, err := os.Stat(dir); !os.IsNotExist(err) || !strings.Contains(buf.String(), "deleted too") {
		t.Errorf("--purge-local left %s (%v):\n%s", dir, err, buf.String())
	}
	if cmdUninstall(t.TempDir(), []string{"--purge"}) == nil {
		t.Error("an unknown flag was taken")
	}
}

// A failure before the command itself runs, such as the login a sync needs, comes as JSON too.
func TestJSONBeforeCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	defer func(o io.Writer, i func() bool) { out, jsonOut, interactive = o, false, i }(out, interactive)
	interactive = func() bool { return false }
	if err := run([]string{"sync", "--json"}); errorCode(err) != "loginFirst" || !jsonOut {
		t.Errorf("sync --json logged out: %v, JSON %v", err, jsonOut)
	}
}

// The email list waits on aline.team for fresh ✔ marks, so like whoami it runs outside the lock
// and never holds up status or schedule.
func TestEmailListOutsideLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	defer func(i func() bool) { jsonOut, interactive = false, i }(interactive)
	interactive = func() bool { return false }
	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600); err != nil { // another command at work
		t.Fatal(err)
	}
	v, err := jsonStdout(t, func() error { return run([]string{"email", "--json"}) })
	if err != nil || v["emails"] == nil {
		t.Errorf("email --json while locked: %v, %v", v, err)
	}
}

// scan, status <name>, history, remove and export answer --json too; a long scan tells its progress as
// JSON lines on stderr.
func TestRepoCommandsJSON(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	var buf, errBuf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut, jsonOut = o, e, false }(out, errOut)
	out, errOut = &buf, &errBuf
	repo := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/me/app.git"},
		{"-c", "user.email=dev@example.com", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "feat: start"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		if b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	dir := t.TempDir()
	if err := saveConfig(dir, Config{Emails: []string{"dev@example.com"}}); err != nil {
		t.Fatal(err)
	}
	top, err := topLevel(repo) // as remove finds it: /var is /private/var on macOS
	if err != nil {
		t.Fatal(err)
	}
	if err := saveRepos(dir, []Repo{newRepo(top)}); err != nil {
		t.Fatal(err)
	}
	run := func(cmd func(string, []string) error, args ...string) map[string]any {
		t.Helper()
		v, err := jsonStdout(t, func() error { return cmd(dir, append(args, "--json")) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return v
	}

	v := run(cmdScan, "--all")
	if list, _ := v["repositories"].([]any); len(list) != 1 || list[0].(map[string]any)["commits"] != 1.0 {
		t.Errorf("scan --all: %v", v)
	}
	if !strings.Contains(errBuf.String(), `{"progress":{"done":0,"step":"progressScan"`) {
		t.Errorf("no progress line on stderr:\n%s", errBuf.String())
	}
	if v := run(cmdStatus, "app"); v["commits"] != 1.0 || v["namespace"] != "me/app" || v["dependencies"] != nil || v["hooks"] != "off" {
		t.Errorf("status app: %v", v)
	}
	history := historyHeader + "2026-10-22T10:00:00+09:00 gitfolio scan --all\n10:00:00.123456 git.c:463 trace: built-in: git version\n"
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	v = run(cmdHistory)
	if runs, _ := v["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["command"] != "gitfolio scan --all" ||
		runs[0].(map[string]any)["git"].([]any)[0].(map[string]any)["command"] != "git version" {
		t.Errorf("history: %v", v)
	}
	if err := cmdExport(dir, []string{"extra"}); errorCode(err) != "usage" {
		t.Errorf("export with an argument: %v", err)
	}
	// A second copy with the same address (say, the folder moved and was added again): purging one
	// leaves aline.team's copy to the other; purging the last one deletes it there.
	repos, _ := loadRepos(dir)
	twin := filepath.Join(t.TempDir(), "app")
	if err := saveRepos(dir, append(repos, Repo{ID: "twin", Name: "app", Path: twin, Provider: repos[0].Provider, Namespace: repos[0].Namespace})); err != nil {
		t.Fatal(err)
	}
	if v := run(cmdRemove, repo, "--purge"); v["commitsDeleted"] != 1.0 || v["serverDeletionQueued"] != false || v["hooksRestored"] != true {
		t.Errorf("remove --purge of one of two copies: %v", v)
	}
	if st, _ := loadSync(dir); len(st.Deletes) != 0 {
		t.Errorf("deletion queued with a copy left: %v", st.Deletes)
	}
	if v := run(cmdRemove, twin, "--purge"); v["serverDeletionQueued"] != true {
		t.Errorf("remove --purge of the last copy: %v", v)
	}
}
