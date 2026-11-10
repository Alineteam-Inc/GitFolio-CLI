package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHooksEndToEnd installs the hooks with the real binary in a repository that already has a
// pre-push hook, then checks that an agent-made commit is collected and sent to aline.team (a fake
// server) after git push, that the old hook still runs, and that remove restores it. It does so for
// git's own hooks folder and for husky 9's (core.hooksPath = .husky/_).
func TestHooksEndToEnd(t *testing.T) {
	bin := t.TempDir()
	exe := "gitfolio"
	if runtime.GOOS == "windows" {
		exe += ".exe" // hooks run under Git for Windows' sh, which finds gitfolio.exe on PATH
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, exe), ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	t.Run("git hooks", func(t *testing.T) { hooksEndToEnd(t, bin, false) })
	t.Run("husky", func(t *testing.T) { hooksEndToEnd(t, bin, true) })
}

// huskyStub and huskyH are what husky 9 generates in .husky/_ (h trimmed to what matters here):
// each hook runs h, which runs the project's .husky/<hook> with git's arguments and stdin.
const huskyStub = "#!/usr/bin/env sh\n. \"$(dirname \"$0\")/h\""
const huskyH = `#!/usr/bin/env sh
n=$(basename "$0")
s=$(dirname "$(dirname "$0")")/$n
[ ! -f "$s" ] && exit 0
sh -e "$s" "$@"
exit $?
`

func hooksEndToEnd(t *testing.T, bin string, husky bool) {
	home := t.TempDir()
	t.Setenv("HOME", home) // data dir comes from os.UserConfigDir
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData")) // the data folder on Windows
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k := range agentEnv { // the test itself may run inside an AI agent
		t.Setenv(k, "")
	}
	t.Setenv("AI_AGENT", "")
	t.Setenv("AGENT", "")
	f := &fakeAline{token: testToken}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)

	remoteDir, repo := t.TempDir(), t.TempDir()
	run := func(env []string, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(nil, remoteDir, "git", "init", "-q", "--bare", "-b", "main")
	run(nil, repo, "git", "init", "-q", "-b", "main")
	run(nil, repo, "git", "config", "user.email", "me@example.com")
	run(nil, repo, "git", "config", "user.name", "me")
	// The fetch URL names a GitHub repository (its namespace is what aline.team gets); pushes go to remoteDir.
	run(nil, repo, "git", "remote", "add", "origin", "https://github.com/me/demo.git")
	run(nil, repo, "git", "remote", "set-url", "--push", "origin", remoteDir)
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// orig is the pre-push hook that was there before gitfolio; it leaves orig-ran next to itself.
	hooks := filepath.Join(repo, ".git", "hooks")
	orig, origScript := filepath.Join(hooks, "pre-push"), "#!/bin/sh\ntouch \"$(dirname \"$0\")/orig-ran\"\n"
	if husky { // the project's own hook; reading a line checks that git's stdin still reaches it
		hooks, orig = filepath.Join(repo, ".husky", "_"), filepath.Join(repo, ".husky", "pre-push")
		origScript = "read line\ntouch \"$(dirname \"$0\")/orig-ran\"\n"
		write(filepath.Join(hooks, "h"), huskyH)
		write(filepath.Join(hooks, ".gitignore"), "*")
		write(filepath.Join(hooks, "pre-push"), huskyStub)
		write(filepath.Join(hooks, "post-commit"), huskyStub)
		run(nil, repo, "git", "config", "core.hooksPath", ".husky/_")
	}
	write(orig, origScript)
	ran := filepath.Join(filepath.Dir(orig), "orig-ran")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, repo, "git", "add", ".")
	run(nil, repo, "git", "commit", "-q", "-m", "plain")
	run(nil, repo, "git", "push", "-q", "-u", "origin", "main")

	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveCredentials(dir, Credentials{Token: testToken}); err != nil { // nothing is collected before login
		t.Fatal(err)
	}
	if err := saveConfig(dir, Config{}); err != nil { // automatic sync: pushes are sent right away
		t.Fatal(err)
	}
	run(nil, repo, "gitfolio", "add", ".")
	if b, _ := os.ReadFile(filepath.Join(hooks, "pre-push")); !strings.Contains(string(b), hookMarker) {
		t.Fatal("pre-push hook not installed")
	}
	if husky { // npm install makes husky write its files again; the next scan puts gitfolio's lines back
		write(filepath.Join(hooks, "pre-push"), huskyStub)
		run(nil, repo, "gitfolio", "scan", ".")
		if b, _ := os.ReadFile(filepath.Join(hooks, "pre-push")); !strings.Contains(string(b), hookMarker) {
			t.Fatal("scan did not put the hook back into husky's pre-push")
		}
	}
	os.Remove(ran)

	// A commit made from an AI agent's shell: no trailer, only the agent's environment variable.
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, repo, "git", "add", ".")
	run([]string{"CLAUDECODE=1"}, repo, "git", "commit", "-q", "-m", "agent change")
	run(nil, repo, "git", "push", "-q", "origin", "main")
	if _, err := os.Stat(ran); err != nil {
		t.Error("the pre-existing pre-push hook did not run")
	}

	var got *Commit
	for deadline := time.Now().Add(15 * time.Second); got == nil && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		commits, _ := readCommits(dir)
		for i := range commits {
			if commits[i].Message == "agent change" {
				got = &commits[i]
			}
		}
	}
	if got == nil {
		t.Fatal("pushed commit was not collected by the pre-push hook")
	}
	if got.CreationType != "HUMAN_CO_AI" || strings.Join(got.AIAgents, ",") != "claude-code" {
		t.Errorf("agent change = %s %v, want HUMAN_CO_AI [claude-code]", got.CreationType, got.AIAgents)
	}
	sent := 0 // both commits: the one collected by add and the pushed one
	for deadline := time.Now().Add(15 * time.Second); sent < 2 && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		f.mu.Lock()
		sent = len(f.commits)
		_, pushed := f.commits["GITHUB/me/demo/"+got.Hash]
		f.mu.Unlock()
		if sent == 2 && !pushed {
			t.Fatal("the pushed commit is not among those sent")
		}
	}
	if sent != 2 {
		t.Fatalf("aline.team got %d commits after the push, want 2", sent)
	}

	run(nil, repo, "gitfolio", "remove", ".")
	if husky {
		for _, name := range []string{"pre-push", "post-commit"} {
			if b, _ := os.ReadFile(filepath.Join(hooks, name)); string(b) != huskyStub {
				t.Errorf("remove did not restore husky's %s:\n%s", name, b)
			}
		}
		return
	}
	b, _ := os.ReadFile(filepath.Join(hooks, "pre-push"))
	if strings.Contains(string(b), hookMarker) || !strings.Contains(string(b), "orig-ran") {
		t.Errorf("remove did not restore the original pre-push hook:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(hooks, "post-commit")); err == nil {
		t.Error("post-commit hook left behind after remove")
	}
}

// Before login nothing is collected: collecting commands fail with a hint and hooks do nothing,
// while settings and cleanup still work (DESIGN 6.1).
func TestLoginGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData")) // the data folder on Windows
	t.Setenv("CLAUDECODE", "1")                         // post-commit would record this tag if it ran
	t.Chdir(t.TempDir())                                // not a git repository: a hook that ran would fail
	if err := run([]string{"scan"}); err == nil || !strings.Contains(err.Error(), "gitfolio login") {
		t.Errorf("scan before login: %v, want a login hint", err)
	}
	if err := run([]string{"deps", "review"}); err == nil {
		t.Error("deps review ran before login")
	}
	for _, args := range [][]string{{"hook", "post-commit"}, {"hook", "push-wait"}, {"config", "api-url", "default"}, {"deps", "off"}} {
		if err := run(args); err != nil {
			t.Errorf("%v before login: %v", args, err)
		}
	}
}

// A file lock left behind is taken while holding lock.os: at once when a run of this release left it,
// after 10 minutes when an older release did (0.6.3 writes its process ID, earlier ones nothing).
// One that an older release may still hold is waited for.
func TestLockLeftBehind(t *testing.T) {
	for _, c := range []struct {
		name, content string
		age           time.Duration
		taken         bool
	}{
		{"this release", lockMark, 0, true},
		{"0.6.3", "12345", 0, false},
		{"0.6.3, 10 minutes", "12345", 11 * time.Minute, true},
		{"0.6.2", "", 0, false},
		{"0.6.2, 10 minutes", "", 11 * time.Minute, true},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "lock")
		if err := os.WriteFile(p, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-c.age)
		os.Chtimes(p, at, at)
		ran := make(chan error)
		go func() { ran <- withLock(dir, func() error { return nil }) }()
		select {
		case err := <-ran:
			if !c.taken || err != nil {
				t.Errorf("%s: taken (%v)", c.name, err)
			}
		case <-time.After(500 * time.Millisecond):
			if c.taken {
				t.Errorf("%s: still waiting", c.name)
			}
			os.Remove(p) // the older release is done
			if err := <-ran; err != nil {
				t.Errorf("%s: after the lock went: %v", c.name, err)
			}
		}
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s: lock left", c.name)
		}
	}
}

// The lock goes with its process: while a run holds it the next one waits, and once that run is killed
// the next one goes on at once, past the file lock it left.
func TestLockGoesWithProcess(t *testing.T) {
	if dir := os.Getenv("GITFOLIO_LOCK_HOLD"); dir != "" { // the run that holds the lock until killed
		withLock(dir, func() error {
			os.WriteFile(filepath.Join(dir, "holding"), nil, 0o600)
			time.Sleep(time.Minute)
			return nil
		})
		return
	}
	dir := t.TempDir()
	holder := exec.Command(os.Args[0], "-test.run=^TestLockGoesWithProcess$")
	holder.Env = append(os.Environ(), "GITFOLIO_LOCK_HOLD="+dir)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Wait()
	for deadline := time.Now().Add(10 * time.Second); !isFile(filepath.Join(dir, "holding")); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			holder.Process.Kill()
			t.Fatal("the holding run did not start")
		}
	}
	ran := make(chan error)
	go func() { ran <- withLock(dir, func() error { return nil }) }()
	select {
	case err := <-ran:
		t.Fatalf("got the lock while another run held it (%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	holder.Process.Kill()
	select {
	case err := <-ran:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("still waiting after the holding run was killed")
	}
}

// Runs that start together take turns, two at a time in each process too, starting over a file lock a
// crashed run left: each adds to a count under the lock, and none of the additions is lost.
func TestLockTakesTurns(t *testing.T) {
	if dir := os.Getenv("GITFOLIO_LOCK_TEST"); dir != "" { // one of the runs
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				for range 20 {
					if err := withLock(dir, func() error {
						b, _ := os.ReadFile(filepath.Join(dir, "count"))
						n, _ := strconv.Atoi(string(b))
						time.Sleep(time.Millisecond)
						return os.WriteFile(filepath.Join(dir, "count"), []byte(strconv.Itoa(n+1)), 0o600)
					}); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lock"), []byte(lockMark), 0o600); err != nil {
		t.Fatal(err)
	}
	var runs []*exec.Cmd
	for range 6 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockTakesTurns$")
		cmd.Env = append(os.Environ(), "GITFOLIO_LOCK_TEST="+dir)
		cmd.Stdout = new(strings.Builder) // what a run that fails says
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, cmd)
	}
	for _, cmd := range runs {
		if err := cmd.Wait(); err != nil {
			t.Errorf("%v\n%s", err, cmd.Stdout)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "count")); string(b) != "240" {
		t.Errorf("count %s, want 240", b)
	}
}

// Commands that write the same file outside the lock, such as two saving a login, each write a temporary
// file of their own: none fails on Unix, and the file is always one of theirs whole.
func TestWriteFileTogether(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 50 {
				err := writeFile(p, []byte(strings.Repeat(strconv.Itoa(i), 4096)))
				if err != nil && runtime.GOOS != "windows" { // Windows may refuse a replace while another runs
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if b, _ := os.ReadFile(p); len(b) != 4096 || strings.Count(string(b), string(b[:1])) != 4096 {
		t.Errorf("not one whole write: %d bytes", len(b))
	}
	if left, _ := filepath.Glob(p + ".*"); len(left) > 0 {
		t.Errorf("left %v", left)
	}
}

// A push from a linked worktree (git worktree add) runs the registered repository's hooks: its commits
// are collected under that repository.
func TestPushFromWorktree(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	base, dir := t.TempDir(), t.TempDir()
	up, main, feat := filepath.Join(base, "up.git"), filepath.Join(base, "app"), filepath.Join(base, "feat")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-c", "user.name=me", "-c", "user.email=me@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "--bare", up)
	git("clone", "-q", up, main)
	git("-C", main, "config", "user.email", "me@example.com")
	git("-C", main, "commit", "-q", "--allow-empty", "-m", "feat: start")
	git("-C", main, "push", "-q", "origin", "HEAD:main")
	git("-C", main, "worktree", "add", "-q", "-b", "feat", feat)
	git("-C", feat, "commit", "-q", "--allow-empty", "-m", "feat: from the worktree")
	git("-C", feat, "push", "-q", "origin", "feat")
	top, err := topLevel(main)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mainWorktree(main); ok {
		t.Error("the main worktree took for a linked one")
	}
	if err := saveConfig(dir, Config{Emails: []string{"me@example.com"}, AutoSyncOff: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveRepos(dir, []Repo{newRepo(top)}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(feat)
	if err := cmdHook(dir, []string{"push-wait"}); err != nil {
		t.Fatal(err)
	}
	commits, err := readCommits(dir)
	if err != nil || !slices.ContainsFunc(commits, func(c Commit) bool { return c.Message == "feat: from the worktree" }) {
		t.Errorf("collected %d commits (%v), want the worktree's", len(commits), err)
	}
}
