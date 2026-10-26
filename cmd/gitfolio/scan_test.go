package main

import (
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Files are sent by their path in the repository, the new one of a rename, whatever characters it has
// (git quotes some, and a name may hold " => " or braces), from both the file names of everyone's history
// (gitLog) and the changed lines of the commits stored (fileStats).
func TestFilePaths(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=me", "-c", "user.email=me@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	names := []string{`q"uote.txt`, "ta\tb.txt", `back\slash.txt`, "we => ird.txt", "{br}.txt", "src/a {b}.go", "docs/한글.md", "sp ace.txt", "src/x.go"}
	if runtime.GOOS != "windows" {
		names = append(names, "new\nline.txt")
	} else { // characters Windows does not allow in file names
		names = slices.DeleteFunc(names, func(n string) bool { return strings.ContainsAny(n, `<>:"\|?*`+"\t") })
	}
	write := func(name, body string) {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	for _, n := range names {
		write(n, n+"\n") // no two alike, so each rename is found as itself
	}
	git("add", "-A")
	git("commit", "-q", "-m", "feat: add")
	want := map[string]FileStat{}
	for _, n := range names {
		dir, base := path.Split(n)
		to := dir + "2" + base
		if n == "src/x.go" {
			to = "lib {x}/x.go" // a folder renamed: git shows "{src => lib {x}}/x.go"
		}
		if dir != "" && to != n {
			if err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(path.Dir(to))), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		git("mv", n, to)
		write(to, n+"\nmore\n")
		want[to] = FileStat{Name: to, Add: 1}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "refactor: rename")
	rng := []string{"HEAD"}
	out, err := gitLog(repo, rng, true)
	if err != nil {
		t.Fatal(err)
	}
	all := parseLog(out)
	stats, err := fileStats(repo, []string{all[0].Hash, all[1].Hash})
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range [][]FileStat{all[0].Files, stats[all[0].Hash]} {
		if len(files) != len(want) {
			t.Fatalf("files %+v", files)
		}
		for _, f := range files {
			w := want[f.Name]
			if f.Name != w.Name || (f.Add != 0 && f.Add != w.Add) || f.Created {
				t.Errorf("renamed: %+v, want %+v", f, w)
			}
		}
	}
	for _, files := range [][]FileStat{all[1].Files, stats[all[1].Hash]} { // the first commit created them
		got := map[string]bool{}
		for _, f := range files {
			got[f.Name] = f.Created
		}
		for _, n := range names {
			if !got[n] {
				t.Errorf("created %q: %v", n, got)
			}
		}
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Alineteam-Inc/GitFolio-CLI.git":    "GITHUB Alineteam-Inc/GitFolio-CLI",
		"https://x-access-token:ghp_secret@github.com/o/r.git": "GITHUB o/r",
		"git@github.com:Alineteam-Inc/GitFolio-CLI.git":        "GITHUB Alineteam-Inc/GitFolio-CLI",
		"ssh://git@GitHub.com:22/o/r.git":                      "GITHUB o/r",
		"git@gitlab.com:group/sub/project.git":                 "GITLAB group/sub/project",
		"https://gitlab.company.internal/group/project/":       "OTHER group/project",
		// Azure DevOps: always organization/project/repository.
		"https://dev.azure.com/org/Proj/_git/repo":                          "DEVOPS org/Proj/repo",
		"https://org@dev.azure.com/org/My%20Project/_git/repo":              "DEVOPS org/My Project/repo",
		"git@ssh.dev.azure.com:v3/org/My%20Project/repo":                    "DEVOPS org/My Project/repo",
		"https://org.visualstudio.com/Proj/_git/repo":                       "DEVOPS org/Proj/repo",
		"https://org.visualstudio.com/DefaultCollection/Proj/_git/repo.git": "DEVOPS org/Proj/repo",
		"org@vs-ssh.visualstudio.com:v3/org/Proj/repo":                      "DEVOPS org/Proj/repo",
		"https://dev.azure.com/org/_git/repo":                               "DEVOPS org/repo/repo",
		"/srv/git/repo.git":                                                 " ",
		"file:///srv/git/repo.git":                                          " ",
		`C:\work\repo`:                                                      " ",
	} {
		provider, ns := parseRemote(in)
		if got := provider + " " + ns; got != want {
			t.Errorf("parseRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// webURL builds a repository address from provider and namespace the way aline.team does (confirmed by
// the server 2026-09-29): each path segment is percent-encoded, "/" stays a separator.
func webURL(provider, namespace string) string {
	seg := strings.Split(namespace, "/")
	for i := range seg {
		seg[i] = url.PathEscape(seg[i])
	}
	switch provider {
	case "GITHUB":
		return "https://github.com/" + strings.Join(seg, "/")
	case "GITLAB":
		return "https://gitlab.com/" + strings.Join(seg, "/")
	case "BITBUCKET":
		return "https://bitbucket.org/" + strings.Join(seg, "/")
	case "DEVOPS":
		if len(seg) == 3 {
			return "https://dev.azure.com/" + seg[0] + "/" + seg[1] + "/_git/" + seg[2]
		}
		return "https://dev.azure.com/" + strings.Join(seg, "/")
	}
	return "" // OTHER has no address
}

// Whatever form a clone URL takes, the provider and namespace sent must give the address the git
// service's web UI shows when sharing the repository.
func TestRemoteGivesWebURL(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:Alineteam-Inc/GitFolio-CLI.git":                 "https://github.com/Alineteam-Inc/GitFolio-CLI",
		"https://x-access-token:ghp_secret@github.com/o/r.git":          "https://github.com/o/r",
		"git@github.com:me/me.github.io.git":                            "https://github.com/me/me.github.io",
		"https://github.com/me/me.github.io":                            "https://github.com/me/me.github.io",
		"ssh://git@ssh.github.com:443/o/r.git":                          "https://github.com/o/r",
		"git@gitlab.com:group/sub/project.git":                          "https://gitlab.com/group/sub/project",
		"https://oauth2:glpat-x@gitlab.com/group/project.git/":          "https://gitlab.com/group/project",
		"git@bitbucket.org:workspace/repo.git":                          "https://bitbucket.org/workspace/repo",
		"https://user@bitbucket.org/workspace/repo.git":                 "https://bitbucket.org/workspace/repo",
		"https://org@dev.azure.com/org/My%20Project/_git/repo":          "https://dev.azure.com/org/My%20Project/_git/repo",
		"https://dev.azure.com/org/%ED%95%9C%EA%B8%80/_git/repo":        "https://dev.azure.com/org/%ED%95%9C%EA%B8%80/_git/repo",
		"git@ssh.dev.azure.com:v3/org/My%20Project/repo":                "https://dev.azure.com/org/My%20Project/_git/repo",
		"https://org.visualstudio.com/DefaultCollection/Proj/_git/repo": "https://dev.azure.com/org/Proj/_git/repo",
		"org@vs-ssh.visualstudio.com:v3/org/Proj/repo":                  "https://dev.azure.com/org/Proj/_git/repo",
		"https://dev.azure.com/org/_git/repo":                           "https://dev.azure.com/org/repo/_git/repo",
		"https://gitlab.company.internal/group/project.git":             "",
	} {
		if got := webURL(parseRemote(remote)); got != want {
			t.Errorf("%s → %q, want %q", remote, got, want)
		}
	}
}

// TestScanRepo builds a real repository with the user's, someone else's, binary,
// renamed and merge commits, then checks what gets stored and that rescans add nothing.
func TestScanRepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, data := t.TempDir(), t.TempDir()

	me := []string{"GIT_AUTHOR_NAME=me", "GIT_AUTHOR_EMAIL=Me@Example.com", "GIT_COMMITTER_NAME=me", "GIT_COMMITTER_EMAIL=me@example.com"}
	other := []string{"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=other@example.com", "GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=other@example.com"}
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(nil, "init", "-q", "-b", "main")
	run(nil, "config", "user.email", "me@example.com")
	write("src/a.go", "1\n2\n3\n")
	write("logo.png", "\x00\x01\x02")
	run(me, "add", ".")
	run(me, "commit", "-q", "-m", "first\n\nbody line")
	write("other.txt", "x\n")
	run(other, "add", ".")
	run(other, "commit", "-q", "-m", "other")
	run(me, "mv", "src/a.go", "src/b.go")
	run(me, "commit", "-q", "-m", "rename")
	run(me, "checkout", "-q", "-b", "side")
	write("c.txt", "x\n")
	run(me, "add", ".")
	run(me, "commit", "-q", "-m", "side\n\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>")
	run(me, "checkout", "-q", "main")
	run(me, "merge", "-q", "--no-ff", "-m", "merge", "side")
	// An AI agent commits for the user (kept) and for someone else (skipped).
	bot := []string{"GIT_AUTHOR_NAME=Copilot", "GIT_AUTHOR_EMAIL=198982749+Copilot@users.noreply.github.com", "GIT_COMMITTER_NAME=GitHub", "GIT_COMMITTER_EMAIL=noreply@github.com"}
	write("d.txt", "x\n")
	run(bot, "add", ".")
	run(bot, "commit", "-q", "-m", "bot work\n\nCo-authored-by: me <me@example.com>")
	write("e.txt", "x\n")
	run(bot, "add", ".")
	run(bot, "commit", "-q", "-m", "bot for other\n\nCo-authored-by: o <other@example.com>")

	r := newRepo(repo)
	n, err := scanRepo(data, &r, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("first scan added %d commits, want 4 (first, rename, side, bot work)", n)
	}
	if n, err := scanRepo(data, &r, false); err != nil || n != 0 {
		t.Fatalf("rescan added %d commits (err %v), want 0", n, err)
	}
	if n, err := scanRepo(data, &r, true); err != nil || n != 4 {
		t.Fatalf("rebuild added %d commits (err %v), want 4", n, err)
	}

	stored, err := readCommits(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 {
		t.Fatalf("stored %d commits after rebuild, want 4", len(stored))
	}
	byMsg := map[string]Commit{}
	for _, c := range stored {
		byMsg[c.Message] = c
		if len(c.Hash) != 40 {
			t.Errorf("%q: hash %q", c.Message, c.Hash)
		}
	}
	// Trailer emails are masked in storage, but AI detection ran on the raw message before that.
	side := "side\n\nCo-Authored-By: Claude Opus 5.5 <[EMAIL]>"
	botWork := "bot work\n\nCo-authored-by: me <[EMAIL]>"
	for msg, want := range map[string]string{
		"first\n\nbody line": "HUMAN",
		"rename":             "HUMAN",
		side:                 "HUMAN_CO_AI",
		botWork:              "AI_CO_HUMAN",
	} {
		if got := byMsg[msg].CreationType; got != want {
			t.Errorf("%q creationType = %q, want %q", msg, got, want)
		}
	}
	if a := byMsg[side].AIAgents; len(a) != 1 || a[0] != "claude-code" {
		t.Errorf("side aiAgents = %v, want [claude-code]", a)
	}
	if a := byMsg[botWork].AIAgents; len(a) != 1 || a[0] != "copilot" {
		t.Errorf("bot work aiAgents = %v, want [copilot]", a)
	}
	if e := byMsg["rename"].AuthorEmail; e != "Me@Example.com" {
		t.Errorf("rename authorEmail = %q", e)
	}
	fs := func(name string, add, del int) FileStat { return FileStat{Name: name, Add: add, Del: del} }
	created := func(f FileStat) FileStat { f.Created = true; return f }
	want := map[string][]FileStat{
		"first\n\nbody line": {created(fs("logo.png", 0, 0)), created(fs("src/a.go", 3, 0))}, // numstat lists paths sorted
		"rename":             {fs("src/b.go", 0, 0)},                                         // a rename is not a created file
		side:                 {created(fs("c.txt", 1, 0))},
		botWork:              {created(fs("d.txt", 1, 0))},
	}
	for msg, files := range want {
		c, ok := byMsg[msg]
		if !ok {
			t.Errorf("commit %q not stored", msg)
			continue
		}
		if len(c.Files) != len(files) {
			t.Errorf("%q files = %+v, want %+v", msg, c.Files, files)
			continue
		}
		for i := range files {
			if c.Files[i] != files[i] {
				t.Errorf("%q file %d = %+v, want %+v", msg, i, c.Files[i], files[i])
			}
		}
	}

	// A newly blocked word is applied to stored commit messages, and only to them: file names stay.
	if err := cmdConfig(data, []string{"mask", "add", "body", "logo"}); err != nil {
		t.Fatal(err)
	}
	if stored, err = readCommits(data); err != nil {
		t.Fatal(err)
	}
	remasked := false
	for _, c := range stored {
		if c.Message == "first\n\n[REDACTED] line" {
			remasked = true
			if c.Files[0].Name != "logo.png" {
				t.Errorf("file name masked: %+v", c.Files)
			}
		}
	}
	if !remasked {
		t.Errorf("mask add did not reach stored commits: %+v", stored)
	}
}

// Commits stored by an older version, with file names only, are collected again once with their paths.
func TestOldFormatRescanned(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, data := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "me@example.com")
	git("config", "user.name", "me")
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cmd", "app", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "start")
	r := newRepo(repo)
	if _, err := scanRepo(data, &r, false); err != nil {
		t.Fatal(err)
	}
	commits, _ := readCommits(data)
	commits[0].Files[0].Name = "main.go" // as stored before paths were recorded
	if err := writeCommits(data, commits); err != nil {
		t.Fatal(err)
	}
	r.Format = 0
	if _, err := scanRepo(data, &r, false); err != nil {
		t.Fatal(err)
	}
	if commits, _ = readCommits(data); len(commits) != 1 || commits[0].Files[0].Name != "cmd/app/main.go" || r.Format != repoFormat {
		t.Errorf("after rescan: %+v, format %d", commits, r.Format)
	}
}

// The repository is where commits are pushed: a push URL different from the fetch URL wins, and of
// several push URLs the first.
func TestRemoteIsWherePushesGo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	r := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", r}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://gitlab.com/example/example-app-flutter.git")
	if p, ns := remote(r); p != "GITLAB" || ns != "example/example-app-flutter" {
		t.Errorf("fetch URL only: %s %s", p, ns)
	}
	run("remote", "set-url", "--push", "origin", "https://github.com/example-org/Example-App.git")
	run("remote", "set-url", "--add", "--push", "origin", "git@gitlab.com:example/example-app-flutter.git")
	if p, ns := remote(r); p != "GITHUB" || ns != "example-org/Example-App" {
		t.Errorf("fetch from GitLab, push to GitHub (then GitLab): %s %s, want the first push URL", p, ns)
	}
	run("config", "--unset-all", "remote.origin.pushurl")
	run("remote", "set-url", "--push", "origin", t.TempDir()) // a local mirror is no git service
	if p, ns := remote(r); p != "GITLAB" || ns != "example/example-app-flutter" {
		t.Errorf("push to a local folder: %s %s, want the fetch URL", p, ns)
	}
}

// scan notices a new git service address of a registered repository and queues the move.
func TestScanQueuesMove(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	dir, path := t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "init", "-q", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", path, "remote", "add", "origin", "https://github.com/me/renamed.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v %s", err, out)
	}
	if err := saveConfig(dir, Config{Emails: []string{"me@example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := newRepo(path)
	r.Provider, r.Namespace = "GITHUB", "me/old" // as the last scan saw it
	if _, err := scanRepo(dir, &r, false); err != nil {
		t.Fatal(err)
	}
	st, _ := loadSync(dir)
	if r.Namespace != "me/renamed" || len(st.Moves) != 1 || st.Moves[0] != (repoMove{deletion{"GITHUB", "me/old"}, deletion{"GITHUB", "me/renamed"}}) {
		t.Errorf("namespace %q, moves %v", r.Namespace, st.Moves)
	}
	if _, err := scanRepo(dir, &r, false); err != nil { // the same address again: nothing more
		t.Fatal(err)
	}
	if st, _ := loadSync(dir); len(st.Moves) != 1 {
		t.Errorf("moves after a second scan: %v", st.Moves)
	}
}

// An app started from Finder finds only /usr/bin/git, which looks up the Command Line Tools' git on every
// run: gitfolio runs that git directly.
func TestGitSkipsTheStandIn(t *testing.T) {
	if runtime.GOOS != "darwin" || exec.Command("xcode-select", "-p").Run() != nil {
		t.Skip("macOS with the Command Line Tools only")
	}
	defer func(p string) { gitPath = p }(gitPath)
	t.Setenv("PATH", "/usr/bin:/bin")
	if !gitWorks() || gitPath == "git" || !isFile(gitPath) {
		t.Errorf("git = %q", gitPath)
	}
	if out, err := git(".", "--version"); err != nil || !strings.HasPrefix(out, "git version") {
		t.Errorf("%s: %q %v", gitPath, out, err)
	}
}

// What AI tools may see of a repository stays as it is when a scan saves it. 0.6.4 and later keep it too.
func TestLaterSettingsKept(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	dir, repo := t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	stored := `[{"id":"a","path":` + strconv.Quote(repo) + `,"name":"app","ai":{"name":"-","hidden":true}}]`
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	repos, err := loadRepos(dir)
	if err != nil {
		t.Fatal(err)
	}
	readRepo(Config{Emails: []string{"dev@example.com"}}, nil, storedNow{}, &repos[0], false)
	if err := saveRepos(dir, repos); err != nil {
		t.Fatal(err)
	}
	if again, _ := loadRepos(dir); again[0].AI != (aiExposure{Name: anonymousName, Hidden: true}) {
		t.Errorf("saved: ai %+v", again[0].AI)
	}
}
