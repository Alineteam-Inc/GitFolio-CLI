package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDetectLang(t *testing.T) {
	defer func(f func() []string) { osLanguages = f }(osLanguages)
	for _, tc := range []struct {
		env    map[string]string
		device []string // macOS preferred languages; nil elsewhere
		want   string
	}{
		{map[string]string{}, nil, "en"},
		{map[string]string{"LANG": "ko_KR.UTF-8"}, nil, "ko"},
		{map[string]string{"LANG": "ja_JP.UTF-8"}, nil, "ja"},
		{map[string]string{"LANG": "ko_KR.UTF-8", "LC_ALL": "C"}, nil, "en"}, // LC_ALL overrides LANG
		{map[string]string{"LANG": "en_US.UTF-8", "GITFOLIO_LANG": "ja"}, nil, "ja"},
		{map[string]string{"LANG": "fr_FR.UTF-8"}, nil, "en"},
		// The device language wins over a terminal that sets LANG=en_US whatever the device says.
		{map[string]string{"LANG": "en_US.UTF-8"}, []string{"ko-KR", "en-US"}, "ko"},
		{map[string]string{"LANG": "ko_KR.UTF-8"}, []string{"en-US", "ko-KR"}, "en"},
		{map[string]string{}, []string{"zh-Hans-KR", "ja-KR"}, "ja"},              // the first one GitFolio has
		{map[string]string{"LANG": "ko_KR.UTF-8"}, []string{"zh-Hans"}, "ko"},     // none of them: environment
		{map[string]string{"GITFOLIO_LANG": "ko"}, []string{"en-US", "ja"}, "ko"}, // GITFOLIO_LANG always wins
	} {
		osLanguages = func() []string { return tc.device }
		if got := detectLang(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("detectLang(%v, device %v) = %q, want %q", tc.env, tc.device, got, tc.want)
		}
	}
}

func TestParseAppleLanguages(t *testing.T) {
	out := "(\n    \"ko-KR\",\n    en,\n    \"ja-KR\"\n)\n" // `defaults read -g AppleLanguages`
	if got := parseAppleLanguages(out); !slices.Equal(got, []string{"ko-KR", "en", "ja-KR"}) {
		t.Errorf("parseAppleLanguages = %q", got)
	}
}

// Every translation takes the same arguments as the English text, so no language prints %!s(MISSING).
func TestMessageVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%(\[\d+\])?[-+# 0-9.]*[a-zA-Z]`)
	for key, byLang := range messages {
		want := verbs.FindAllString(byLang["en"], -1)
		slices.Sort(want)
		for _, lang := range []string{"ko", "ja"} {
			got := verbs.FindAllString(byLang[lang], -1)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("message %q: %s has %v, en has %v", key, lang, got, want)
			}
		}
	}
	t.Setenv("GITFOLIO_LANG", "ko")
	if err := failure("notRegistered", "~/Code/demo"); err.Error() != "등록되지 않은 저장소입니다: ~/Code/demo" {
		t.Errorf("failure = %q", err)
	}
}

func TestMessagesComplete(t *testing.T) {
	for key, byLang := range messages {
		for _, lang := range []string{"en", "ko", "ja"} {
			if byLang[lang] == "" {
				t.Errorf("message %q has no %s text", key, lang)
			}
		}
	}
	for _, line := range strings.Split(header(), "\n") {
		if len(line) > 80 {
			t.Errorf("header line wider than 80 columns: %q", line)
		}
	}
}

func TestSuggestRoots(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Code", "Documents/GitHub", "Music"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := suggestRoots(home) // "code" must not show up twice on case-insensitive disks
	want := []string{filepath.Join(home, "Code"), filepath.Join(home, "Documents", "GitHub")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("suggestRoots = %v, want %v", got, want)
	}
}

// TestFindRepos builds a folder tree with repositories where they should and should not be found.
func TestFindRepos(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	gitIn := func(dir string, args ...string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=me", "GIT_AUTHOR_EMAIL=me@example.com", "GIT_COMMITTER_NAME=me", "GIT_COMMITTER_EMAIL=me@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, d := range []string{"a", "group/b", "a/nested", ".hidden/c", "node_modules/d", "1/2/3/4/5/deep"} {
		gitIn(filepath.Join(root, d), "init", "-q", "-b", "main")
	}
	gitIn(filepath.Join(root, "a"), "config", "user.email", "me@example.com")
	gitIn(filepath.Join(root, "a"), "commit", "-q", "--allow-empty", "-m", "mine")
	gitIn(filepath.Join(root, "a"), "worktree", "add", "-q", filepath.Join(root, "a-wt"))

	var names []string
	for _, p := range findRepos([]string{root}, 5) {
		rel, _ := filepath.Rel(root, p)
		if r, err := filepath.EvalSymlinks(root); err == nil { // macOS temp dirs sit behind /var -> /private/var
			if rr, err := filepath.Rel(r, p); err == nil && !strings.HasPrefix(rr, "..") {
				rel = rr
			}
		}
		names = append(names, filepath.ToSlash(rel))
	}
	// a-wt is a worktree of a; a/nested is inside a; .hidden, node_modules and depth 6 are skipped.
	if got := strings.Join(names, " "); got != "a group/b" {
		t.Errorf("findRepos = %q, want %q", got, "a group/b")
	}
	if n, last := ownCommits(filepath.Join(root, "a"), nil); n != 1 || len(last) != 10 {
		t.Errorf("ownCommits = %d, %q; want 1 and a YYYY-MM-DD date", n, last)
	}
}

// The token file must be readable by its owner only.
func TestCredentialsPermission(t *testing.T) {
	dir := t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, "credentials.json"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("credentials.json mode = %v, want 0600", st.Mode().Perm())
		}
	}
}

// Status lines printed one after another form one paragraph with a single prefix; a notice, a question,
// a section or a blank line starts a new one.
func TestStatusParagraphs(t *testing.T) {
	var b strings.Builder
	inParagraph = false
	show(&b, "first")
	show(&b, "second\nthird")
	inParagraph = false // what notice, prompt, section and blank do
	show(&b, "fourth")
	under := strings.Repeat(" ", len(statusPrefix))
	want := margin + statusPrefix + "first\n" + margin + under + "second\n" + margin + under + "third\n" + margin + statusPrefix + "fourth\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
	inParagraph = false
}

// TestScreenLayout: paragraphs are separated by exactly one empty line however they are printed,
// and settings line up by display width (Korean takes two columns per character).
func TestScreenLayout(t *testing.T) {
	var b strings.Builder
	saved, savedNL := out, newlines
	t.Cleanup(func() { out, newlines, inParagraph = saved, savedNL, false })
	out, newlines, inParagraph = screen{&b}, 2, false

	notice("Explanation.\n")
	say("en", "upToDate")
	blank()
	blank()
	section("en", "settingsTitle")
	want := "  Explanation.\n\n  GitFolio : " + tr("en", "upToDate") + "\n  ===== GitFolio · Settings =====\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}

	got := settings("Done.\nRepositories: 2\n대표 이메일: me@example.com\n")
	if want := "Done.\nRepositories  2\n대표 이메일   me@example.com\n"; got != want {
		t.Errorf("settings =\n%q\nwant\n%q", got, want)
	}
}

func TestProgress(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, color, nl := os.Stdout, useColor, newlines
	t.Cleanup(func() { os.Stdout, useColor, newlines, progressShown = stdout, color, nl, false })
	os.Stdout = w

	useColor = false
	progress("en", "progressSend", 1, 2) // not a terminal: nothing
	useColor = true
	progress("en", "progressSend", 3, 10)
	_, _ = screen{io.Discard}.Write([]byte("next\n")) // the next output clears the line first
	progressDone()                                    // already cleared: nothing
	w.Close()
	got, _ := io.ReadAll(r)
	want := "\r\x1b[2K  ℹ Syncing...  \x1b[36m██████\x1b[0m\x1b[2m░░░░░░░░░░░░░░\x1b[0m  30%\r\x1b[2K"
	if string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// init says the chosen repositories get git hooks, then asks once what AI coding tools see of them.
func TestAskRepoChoices(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(r *bufio.Reader, o io.Writer) { stdin, out = r, o }(stdin, out)
	out = &buf
	for answers, want := range map[string]aiExposure{
		"\n\n":      {}, // Enter: real name, nothing more
		"2\n2\n":    {Name: anonymousName, Messages: "subjects", Branches: true},
		"3\n":       {Hidden: true},                     // hidden: nothing more asked
		"9\n1\n3\n": {Messages: "full", Branches: true}, // asked again after a wrong answer
	} {
		stdinClosed, stdin = false, bufio.NewReader(strings.NewReader(answers))
		if ai := askRepoChoices("en"); ai != want {
			t.Errorf("answers %q: ai %+v; want %+v", answers, ai, want)
		}
	}
	if !strings.Contains(buf.String(), "nothing is added to the repository or its remote") {
		t.Errorf("hooks not told:\n%s", buf.String())
	}
}

// Before any settings are saved GitFolio is in local mode; settings saved earlier keep automatic sync.
func TestLocalModeByDefault(t *testing.T) {
	dir := t.TempDir()
	if cfg, err := loadConfig(dir); err != nil || !cfg.AutoSyncOff || !cfg.LocalModeKnown {
		t.Errorf("new settings = %+v, %v; want local mode", cfg, err)
	}
	if err := saveConfig(dir, Config{Emails: []string{"me@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := loadConfig(dir); cfg.AutoSyncOff {
		t.Error("saved automatic sync read back as local mode")
	}
}

// A program walks through what init asks, without questions: the data policy, the search (only after
// logging in), the work emails and their codes, the repositories, the package manager files, the
// schedule and the sync mode, each answered with JSON.
func TestSetupJSON(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &fakeAline{token: testToken}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	var buf bytes.Buffer
	defer func(o, e io.Writer, i func() bool) { out, errOut, jsonOut, interactive = o, e, false, i }(out, errOut, interactive)
	out, errOut, interactive = &buf, &buf, func() bool { return false }
	root := t.TempDir()
	app, lib := filepath.Join(root, "app"), filepath.Join(root, "lib")
	gitIn := func(repo string, args ...string) {
		t.Helper()
		if b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	for _, r := range []string{app, lib} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		gitIn(r, "init", "-q", "-b", "main")
	}
	gitIn(app, "config", "user.email", "dev@example.com")
	gitIn(app, "config", "user.name", "dev")
	if err := os.WriteFile(filepath.Join(app, "package.json"), []byte(`{"dependencies":{"left-pad":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(app, "add", "package.json")
	gitIn(app, "commit", "-q", "-m", "feat: start")
	// lib sets no email of its own: the user's commit there counts by the email app uses.
	gitIn(lib, "-c", "user.email=other@example.com", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "theirs")
	gitIn(lib, "-c", "user.email=dev@example.com", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "mine")
	dir := t.TempDir()
	run := func(cmd func(string, []string) error, args ...string) (map[string]any, error) {
		t.Helper()
		return jsonStdout(t, func() error { return cmd(dir, append(args, "--json")) })
	}
	must := func(v map[string]any, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if v := must(run(cmdInit, "--policy")); !strings.Contains(fmt.Sprint(v["policy"]), "Source code is never collected") ||
		!strings.Contains(fmt.Sprint(v["setupActions"]), "on this computer only") {
		t.Errorf("policy: %v", v)
	}
	if _, err := run(cmdInit, "--scan", "--root", root); errorCode(err) != "loginFirst" {
		t.Errorf("scan logged out: %v", err)
	}
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}); err != nil {
		t.Fatal(err)
	}
	v := must(run(cmdInit, "--scan", "--root", root))
	repos, _ := v["repositories"].([]any)
	emails, _ := v["emails"].([]any)
	own := map[string]any{}
	for _, r := range repos {
		own[r.(map[string]any)["name"].(string)] = r.(map[string]any)["ownCommits"]
	}
	if own["app"] != 1.0 || own["lib"] != 1.0 || len(emails) != 1 || emails[0].(map[string]any)["email"] != "dev@example.com" {
		t.Errorf("scan: %v", v)
	}
	if cfg, _ := loadConfig(dir); !slices.Equal(cfg.Roots, []string{root}) {
		t.Errorf("roots kept: %v", cfg.Roots)
	}
	if _, err := run(cmdInit, "--scan", "--root", filepath.Join(root, "none")); errorCode(err) != "rootMissing" {
		t.Errorf("missing root: %v", err)
	}

	must(run(cmdEmail, "add", "dev@example.com"))
	if v := must(run(cmdEmail, "verify", "me@work.com")); v["verified"] != false || v["codeLength"] != 6.0 {
		t.Errorf("code sent: %v", v)
	}
	v = must(run(cmdEmail, "verify", "me@work.com", "--code", "123456"))
	if list, _ := v["emails"].([]any); len(list) != 2 || list[1].(map[string]any)["verified"] != true || list[0].(map[string]any)["primary"] != true {
		t.Errorf("verified: %v", v)
	}

	if v := must(run(cmdAdd, app)); v["name"] != "app" || v["commits"] != 1.0 {
		t.Errorf("add: %v", v)
	}
	v = must(run(cmdDeps, "on"))
	files := v["repositories"].([]any)[0].(map[string]any)["files"].([]any)
	if v["on"] != true || len(files) != 1 || files[0].(map[string]any)["state"] != "new" {
		t.Errorf("deps on: %v", v)
	}
	v = must(run(cmdDeps, "review", app, "--allow", "package.json"))
	if files := v["repositories"].([]any)[0].(map[string]any)["files"].([]any); files[0].(map[string]any)["state"] != "allowed" {
		t.Errorf("deps review: %v", v)
	}
	if deps, _ := loadDeps(dir); len(deps) != 1 {
		t.Errorf("dependencies read: %v", deps)
	}
	if commits, _ := readCommits(dir); len(commits) != 1 || commits[0].Files[0].Module != moduleID("") {
		t.Errorf("stored commits not put in the approved module: %v", commits)
	}
	// A typo is refused: taken as no file at all, it would decline every one.
	if _, err := run(cmdDeps, "review", app, "--alow", "package.json"); errorCode(err) != "didYouMeanWord" {
		t.Errorf("mistyped --allow: %v", err)
	}
	if repos, _ := loadRepos(dir); !repos[0].Manifests["package.json"] {
		t.Errorf("approval after a refused typo: %v", repos[0].Manifests)
	}
	if v := must(run(cmdDeps, "review", app, "--allow=package.json")); v["repositories"].([]any)[0].(map[string]any)["files"].([]any)[0].(map[string]any)["state"] != "allowed" {
		t.Errorf("--allow=: %v", v)
	}
	if _, err := run(cmdDeps, "review", app, "--allow", "nope.json"); errorCode(err) != "manifestUnknown" {
		t.Errorf("unknown file: %v", err)
	}

	if v := must(run(cmdSchedule)); v["schedule"] != "" {
		t.Errorf("schedule: %v", v)
	}
	if v := must(run(cmdConfig, "autosync", "off")); v["autoSyncOff"] != true || v["localModeKnown"] != true {
		t.Errorf("config autosync off: %v", v)
	}
}

// With --json the text a program shows (error messages, the policy, the sign-up notice, progress) names no
// gitfolio command, flag or "CLI": every failure whose text does has an app wording with the same arguments.
func TestAppMessages(t *testing.T) {
	command := regexp.MustCompile("gitfolio [a-z]|`gitfolio`|(^|[^\\w-])--[a-z]|\\bCLI\\b")
	verbs := regexp.MustCompile(`%(\[\d+\])?[-+# 0-9.]*[a-zA-Z]`)
	names := func(s string) []string { v := verbs.FindAllString(s, -1); slices.Sort(v); return v }
	for key, byLang := range appMessages {
		base, ok := messages[key]
		if !ok {
			t.Errorf("app message %q has no message", key)
		}
		for _, lang := range []string{"en", "ko", "ja"} {
			switch s := byLang[lang]; {
			case s == "":
				t.Errorf("app message %q has no %s text", key, lang)
			case command.MatchString(s):
				t.Errorf("app message %q (%s) names a command: %q", key, lang, s)
			case !slices.Equal(names(s), names(base["en"])):
				t.Errorf("app message %q (%s) has %v, the message has %v", key, lang, names(s), names(base["en"]))
			}
		}
	}
	src, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	failures := regexp.MustCompile(`failure\("(\w+)"`)
	keys := []string{"relogin", "policy", "setupActions", "signupNotice", "notifyAsk", "progressScan", "progressSend", "gitInstallManual",
		"gitUpdateApple", "gitUpdateMac", "gitUpdateWindows", "gitUpdateLinux"} // put into JSON
	for _, f := range src {
		if strings.HasSuffix(f.Name(), ".go") && !strings.HasSuffix(f.Name(), "_test.go") {
			b, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range failures.FindAllStringSubmatch(string(b), -1) {
				keys = append(keys, m[1])
			}
		}
	}
	defer func() { jsonOut = false }()
	jsonOut = true
	for _, key := range keys {
		for _, lang := range []string{"en", "ko", "ja"} {
			if s := tr(lang, key); key != "usage" && command.MatchString(s) {
				t.Errorf("with --json, %q (%s) names a command: %q", key, lang, s)
			}
		}
	}
	if !strings.Contains(failure("loginFirst").Error(), "first.") || jsonOut && strings.Contains(tr("en", "relogin"), "gitfolio") {
		t.Errorf("--json does not take the app wording: %q", failure("loginFirst"))
	}
	jsonOut = false
	if !strings.Contains(failure("loginFirst").Error(), "gitfolio login") {
		t.Errorf("the terminal lost the command: %q", failure("loginFirst"))
	}
}

// git update goes the way git was installed: brew for Homebrew's, winget on Windows, Software Update
// for Apple's Command Line Tools and the App Store for Xcode's; anything else is updated by hand.
func TestGitUpdater(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(n string) bool { return slices.Contains(names, n) }
	}
	for _, c := range []struct {
		goos, path, tools string
		has               func(string) bool
		want, how         string
	}{
		{"darwin", "/usr/bin/git", "/Library/Developer/CommandLineTools/usr/bin/git", has(),
			"open x-apple.systempreferences:com.apple.Software-Update-Settings.extension", "softwareUpdate"},
		{"darwin", "/usr/bin/git", "/Applications/Xcode.app/Contents/Developer/usr/bin/git", has(),
			"open macappstore://showUpdatesPage", "appStore"},
		{"windows", `C:\Program Files\Git\cmd\git.exe`, "git", has("winget"),
			"winget upgrade --id Git.Git -e --source winget --accept-package-agreements --accept-source-agreements", "winget"},
		{"windows", `C:\Program Files\Git\cmd\git.exe`, "git", has(), "", "manual"},
		{"linux", "/usr/bin/git", "git", has("apt-get", "sudo"), "", "manual"},
	} {
		args, how := gitUpdater(c.goos, c.path, c.tools, c.has)
		if got := strings.Join(args, " "); got != c.want || how != c.how {
			t.Errorf("%s %s: %q %s, want %q %s", c.goos, c.tools, got, how, c.want, c.how)
		}
	}
	if runtime.GOOS == "windows" {
		return // a symlink there takes administrator rights
	}
	prefix := t.TempDir()
	real := filepath.Join(prefix, "Cellar", "git", "2.55.0", "bin", "git")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(real, nil, 0o755)
	os.MkdirAll(filepath.Join(prefix, "bin"), 0o755)
	if err := os.Symlink(real, filepath.Join(prefix, "bin", "git")); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(prefix) // macOS: /var is /private/var
	args, how := gitUpdater("darwin", filepath.Join(prefix, "bin", "git"), "git", has())
	if want := filepath.Join(resolved, "bin", "brew") + " upgrade git"; strings.Join(args, " ") != want || how != "brew" {
		t.Errorf("Homebrew's git: %q %s, want %q brew", strings.Join(args, " "), how, want)
	}
}

// Without git, init offers the system's own installer: the Command Line Tools on macOS, winget on
// Windows, the package manager on Linux through sudo unless root; nothing when there is none to use.
func TestGitInstaller(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(n string) bool { return slices.Contains(names, n) }
	}
	for _, c := range []struct {
		goos string
		has  func(string) bool
		root bool
		want string
	}{
		{"darwin", has(), false, "xcode-select --install"},
		{"windows", has("winget"), false, "winget install --id Git.Git -e --source winget"},
		{"windows", has(), false, ""},
		{"linux", has("apt-get", "sudo"), false, "sudo sh -c apt-get update && apt-get install -y git"},
		{"linux", has("apt-get"), true, "sh -c apt-get update && apt-get install -y git"},
		{"linux", has("dnf"), false, ""}, // no sudo, not root: the user installs it
		{"linux", has("pacman", "sudo"), false, "sudo pacman -S --noconfirm git"},
		{"linux", has("sudo"), false, ""},
	} {
		if got := strings.Join(gitInstaller(c.goos, c.has, c.root), " "); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.goos, c.root, got, c.want)
		}
	}

	// At the terminal, init asks first; no means nothing runs and git is still missing.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer, i func() bool, r *bufio.Reader, d func() []string) {
		out, errOut, interactive, stdin, gitDirs, jsonOut = o, e, i, r, d, false
	}(out, errOut, interactive, stdin, gitDirs)
	gitDirs = func() []string { return nil } // no git off the PATH either (CI machines have Homebrew's)
	out, errOut, interactive, stdin = &buf, &buf, func() bool { return true }, bufio.NewReader(strings.NewReader("n\n"))
	if err := run([]string{"init"}); errorCode(err) != "noGit" || !strings.Contains(buf.String(), "git is not installed") {
		t.Errorf("init without git: %v\n%s", err, buf.String())
	}
	if asked := strings.Contains(buf.String(), "Install git now"); asked != (runtime.GOOS == "darwin") { // elsewhere none is on this PATH
		t.Errorf("asked %v on %s:\n%s", asked, runtime.GOOS, buf.String())
	}
	buf.Reset()
	if err := run([]string{"status", "--json"}); errorCode(err) != "noGit" || buf.Len() != 0 {
		t.Errorf("a program was asked: %v\n%s", err, buf.String())
	}
	// The data policy comes first, before git is there and without the data folder.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "none"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	t.Setenv("AppData", filepath.Join(os.Getenv("HOME"), "AppData"))
	v, err := jsonStdout(t, func() error { return run([]string{"init", "--policy", "--json"}) })
	if _, serr := os.Stat(os.Getenv("HOME")); err != nil || !strings.Contains(fmt.Sprint(v["policy"]), "Source code is never collected") || serr == nil ||
		!regexp.MustCompile(`on this computer: [^\n]*gitfolio`).MatchString(fmt.Sprint(v["setupActions"])) {
		t.Errorf("policy without git: %v %v (data folder made: %v)", v, err, serr == nil)
	}
}

// gitfolio git --json tells a program whether git is there, without needing it; git installed off the
// PATH (as an app started from Finder sees it) is found and put on it. git install --json starts no
// questions; where it cannot start, it gives the manual way.
func TestGitCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITFOLIO_LANG", "en")
	var buf bytes.Buffer
	defer func(o, e io.Writer, d func() []string) { out, errOut, gitDirs, jsonOut = o, e, d, false }(out, errOut, gitDirs)
	out, errOut, gitDirs = &buf, &buf, func() []string { return nil }

	v, err := jsonStdout(t, func() error { return run([]string{"git", "--json"}) })
	if err != nil || v["found"] != false {
		t.Errorf("git --json without git: %v %v", v, err)
	}
	if runtime.GOOS != "darwin" { // there it would open Apple's install window
		v, err := jsonStdout(t, func() error { return run([]string{"git", "install", "--json"}) })
		if err != nil || v["started"] != false || v["how"] != "manual" || !strings.HasPrefix(fmt.Sprint(v["url"]), "https://git-scm.com/download/") ||
			strings.Contains(fmt.Sprint(v["message"]), "gitfolio") {
			t.Errorf("git install --json without an installer: %v %v", v, err)
		}
	}

	elsewhere := t.TempDir() // git installed where this PATH does not look
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(elsewhere, name), []byte("#!/bin/sh\necho git version 2.99.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitDirs = func() []string { return []string{elsewhere} }
	if v, err := jsonStdout(t, func() error { return run([]string{"git", "install", "--json"}) }); err != nil || v["how"] != "installed" {
		t.Errorf("git install --json with git: %v %v", v, err)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), elsewhere) {
		t.Errorf("git found off the PATH was not put on it: %s", os.Getenv("PATH"))
	}
}

// git --latest compares the upstream release a git is built on with git's newest release tag.
func TestGitLatest(t *testing.T) {
	lsRemote := "a1\trefs/tags/v2.54.0\nb2\trefs/tags/v2.55.0-rc2\nc3\trefs/tags/v2.9.5\nd4\trefs/tags/v2.53.1\ne5\trefs/tags/gitgui-0.10.0\n"
	if got := newestRelease(lsRemote); got != "v2.54.0" {
		t.Errorf("newestRelease = %q, want v2.54.0 (no release candidates, 2.54 > 2.9)", got)
	}
	for in, want := range map[string]string{"2.54.0 (Apple Git-157)": "v2.54.0", "2.47.0.windows.2": "v2.47.0", "2.43.0": "v2.43.0", "unknown": ""} {
		if got := gitRelease(in); got != want {
			t.Errorf("gitRelease(%q) = %q, want %q", in, got, want)
		}
	}
}
