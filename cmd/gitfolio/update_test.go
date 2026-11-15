package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUpdateCheck(t *testing.T) {
	for _, c := range []struct {
		tag, have string
		want      bool
	}{
		{"v0.2.0", "0.1.0", true},
		{"v0.1.1", "0.1.0", true},
		{"v1.0.0", "0.9.9", true},
		{"v0.10.0", "0.9.0", true}, // numbers, not text
		{"v0.1.0", "0.1.0", false},
		{"v0.1.0", "0.2.0", false},
		{"v0.2.0-rc.1", "0.1.0", false}, // pre-releases are not offered
		{"v0.2.0", "dev", false},
	} {
		if got := newer(c.tag, c.have); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.tag, c.have, got, c.want)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Alineteam-Inc/GitFolio-CLI/releases/tag/v0.2.0", http.StatusFound)
	}))
	defer srv.Close()
	defer func(u string) { latestURL = u }(latestURL)
	latestURL = srv.URL
	if tag, err := latestRelease(); err != nil || tag != "v0.2.0" {
		t.Errorf("latestRelease = %q, %v", tag, err)
	}
	// Builds from source never check or ask.
	if checkUpdate(t.TempDir()) {
		t.Error("a dev build offered an update")
	}

	// gitfolio update: nothing to do from source or on the newest release, and today's offer is used up.
	dir := t.TempDir()
	if err := cmdUpdate(dir, nil); err != nil {
		t.Errorf("update from a source build: %v", err)
	}
	defer func(v string) { version = v }(version)
	version = "0.2.0"
	if err := cmdUpdate(dir, nil); err != nil {
		t.Errorf("update on the newest release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, updateFile)); err != nil {
		t.Error("update did not record the check")
	}
}

// The null device is a character device too, but nobody answers there.
func TestNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if terminal(f) {
		t.Error("the null device counts as a terminal")
	}
}

type ttyBuffer struct{ strings.Builder }

func (*ttyBuffer) Close() error { return nil }

// Someone who only pushes is told about a newer release in the terminal the push runs in, at most once
// a day, from what the push's background sync looked up: a push never waits on the network.
func TestUpdateNoticeOnPush(t *testing.T) {
	looks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		looks++
		http.Redirect(w, r, "/Alineteam-Inc/GitFolio-CLI/releases/tag/v0.5.0", http.StatusFound)
	}))
	defer srv.Close()
	defer func(u string) { latestURL = u }(latestURL)
	latestURL = srv.URL
	defer func(v string) { version = v }(version)
	version = "0.4.1"
	t.Setenv("GITFOLIO_LANG", "en")
	tty := &ttyBuffer{}
	defer func(f func() (io.WriteCloser, error)) { ttyOut = f }(ttyOut)
	ttyOut = func() (io.WriteCloser, error) { return tty, nil }
	dir := t.TempDir()

	noticeUpdate(dir) // nothing looked up yet
	if tty.Len() != 0 {
		t.Fatalf("told before any look-up: %q", tty.String())
	}
	if st := lookUpLatest(dir); st.Latest != "v0.5.0" || looks != 1 {
		t.Fatalf("look-up = %+v after %d requests", st, looks)
	}
	lookUpLatest(dir) // the same day: no second request
	if looks != 1 {
		t.Errorf("looked up %d times in a day", looks)
	}
	noticeUpdate(dir)
	if got := tty.String(); !strings.Contains(got, "0.5.0") || !strings.Contains(got, "0.4.1") || !strings.Contains(got, "gitfolio update") {
		t.Errorf("notice = %q", got)
	}
	told := tty.Len()
	noticeUpdate(dir) // once a day
	if tty.Len() != told {
		t.Errorf("told twice in a day: %q", tty.String())
	}
	// The notice does not use up the question an interactive command asks the same day.
	var st updateState
	if err := loadJSON(filepath.Join(dir, updateFile), &st); err != nil || !st.AskedAt.IsZero() {
		t.Errorf("state after the notice: %+v, %v", st, err)
	}

	// No terminal (a GUI git client): nothing is shown, and the next push tries again.
	dir = t.TempDir()
	lookUpLatest(dir)
	ttyOut = func() (io.WriteCloser, error) { return nil, os.ErrNotExist }
	noticeUpdate(dir)
	if err := loadJSON(filepath.Join(dir, updateFile), &st); err != nil || !st.NoticedAt.IsZero() {
		t.Errorf("without a terminal the notice counted as given: %+v", st)
	}
	// On the newest release, or built from source: nothing to tell.
	ttyOut = func() (io.WriteCloser, error) { return tty, nil }
	told = tty.Len()
	for _, v := range []string{"0.5.0", "dev"} {
		version = v
		noticeUpdate(t.TempDir())
		noticeUpdate(dir)
	}
	if tty.Len() != told {
		t.Errorf("told on the newest release or a source build: %q", tty.String()[told:])
	}
}

// The install script replaces this copy where it is; where that is unknown, it never gets "." for it.
func TestInstallEnv(t *testing.T) {
	has := func(env []string, kv string) bool { return slices.Contains(env, kv) }
	if env := installEnv("v0.5.0", filepath.Join("opt", "bin", "gitfolio")); !has(env, "GITFOLIO_VERSION=v0.5.0") || !has(env, "GITFOLIO_INSTALL_DIR="+filepath.Join("opt", "bin")) {
		t.Errorf("known copy: %v", env[len(env)-2:])
	}
	for _, kv := range installEnv("v0.5.0", "") {
		if strings.HasPrefix(kv, "GITFOLIO_INSTALL_DIR=") {
			t.Errorf("unknown copy got %s", kv)
		}
	}
}

// A cask brings only GitFolio's tap up to date, not all of Homebrew; brew update runs only when that fails.
// The cask goes by its full name, also in the command shown to run by hand.
func TestBrewUpdatesItsTap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Homebrew on Windows")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("HOMEBREW_NO_AUTO_UPDATE", "")
	dir := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	up, tap := filepath.Join(dir, "up"), filepath.Join(dir, "tap")
	git("init", "-q", "-b", "main", up)
	git("-C", up, "commit", "-q", "--allow-empty", "-m", "0.6.0")
	git("clone", "-q", up, tap)
	git("-C", up, "commit", "-q", "--allow-empty", "-m", "0.6.1")

	cask := filepath.Join(dir, "brew", "Caskroom", "gitfolio", "0.6.0", "gitfolio")
	brew := filepath.Join(dir, "brew", "bin", "brew")
	for p, s := range map[string]string{cask: "", brew: "#!/bin/sh\n[ \"$1\" = --repository ] && echo \"$TAP\" && exit\necho \"$HOMEBREW_NO_AUTO_UPDATE $*\" >> \"$LOG\"\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	upgrade := "1 upgrade --cask alineteam-inc/tap/gitfolio\n"
	for i, c := range []struct{ tap, want string }{{tap, upgrade}, {filepath.Join(dir, "none"), " update\n" + upgrade}} {
		log := filepath.Join(dir, fmt.Sprint("log", i))
		t.Setenv("TAP", c.tap)
		t.Setenv("LOG", log)
		cmd, manual := updateCommand("v0.6.1", cask)
		if !strings.HasSuffix(manual, " upgrade --cask alineteam-inc/tap/gitfolio") {
			t.Errorf("by hand: %s, want the cask's full name", manual)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tap %s: %v\n%s", c.tap, err, out)
		}
		if got, _ := os.ReadFile(log); string(got) != c.want {
			t.Errorf("tap %s: brew ran\n%s\nwant\n%s", c.tap, got, c.want)
		}
	}
	if got := git("-C", tap, "log", "-1", "--format=%s"); got != "0.6.1" {
		t.Errorf("tap at %s, want 0.6.1", got)
	}
}

// With --json a failed update ends its error with what brew said last, which a program shows; at a
// terminal brew already said it there.
func TestUpdateFailedSaysWhy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Homebrew on Windows")
	}
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	cask := filepath.Join(dir, "brew", "Caskroom", "gitfolio", "0.6.0", "gitfolio")
	brew := filepath.Join(dir, "brew", "bin", "brew")
	for p, s := range map[string]string{cask: "", brew: "#!/bin/sh\n[ \"$1\" = --repository ] && echo " + filepath.Join(dir, "none") + " && exit\necho 'Error: Fetching the tap failed!' >&2\nexit 1\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer func(o io.Writer) { out, jsonOut = o, false }(out)
	out = io.Discard
	for _, j := range []bool{true, false} {
		jsonOut = j
		err := installUpdate("v0.6.1", cask)
		if err == nil || !strings.HasPrefix(err.Error(), "Could not update (exit status 1).") ||
			strings.HasSuffix(err.Error(), "\nError: Fetching the tap failed!") != j {
			t.Errorf("--json %v: %v", j, err)
		}
	}
}

// A copy an app ships never looks up, tells about or installs a newer release: the app updates it.
// version --json says which kind it is.
func TestBundledCopy(t *testing.T) {
	looks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		looks++
		http.Redirect(w, r, "/Alineteam-Inc/GitFolio-CLI/releases/tag/v0.5.0", http.StatusFound)
	}))
	defer srv.Close()
	defer func(u, v, b string, f func() (io.WriteCloser, error)) {
		latestURL, version, bundled, ttyOut = u, v, b, f
	}(latestURL, version, bundled, ttyOut)
	latestURL, version = srv.URL, "0.4.1"
	t.Setenv("GITFOLIO_LANG", "en")
	tty := &ttyBuffer{}
	ttyOut = func() (io.WriteCloser, error) { return tty, nil }
	var buf bytes.Buffer
	defer func(o io.Writer) { out = o }(out)
	out = &buf
	dir := t.TempDir()
	if err := saveJSON(filepath.Join(dir, updateFile), updateState{CheckedAt: time.Now(), Latest: "v0.5.0"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITFOLIO_LANG", "ja")
	if v, err := jsonStdout(t, func() error { return run([]string{"version", "--json"}) }); err != nil || v["version"] != "0.4.1" || v["bundled"] != false || v["lang"] != "ja" {
		t.Errorf("version --json: %v %v", v, err)
	}
	t.Setenv("GITFOLIO_LANG", "en")
	bundled = "yes"
	noticeUpdate(dir)
	if err := cmdUpdate(dir, nil); err != nil || looks != 0 || tty.Len() != 0 || !strings.Contains(buf.String(), "came with an app") {
		t.Errorf("bundled: %d look-ups, told %q, update says %q (%v)", looks, tty.String(), buf.String(), err)
	}
	if v, err := jsonStdout(t, func() error { return run([]string{"version", "--json"}) }); err != nil || v["bundled"] != true {
		t.Errorf("version --json, bundled: %v %v", v, err)
	}
}

// version --json asks for the newest release only with --latest, and never for a copy an app ships.
func TestVersionLatest(t *testing.T) {
	looks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		looks++
		http.Redirect(w, r, "/Alineteam-Inc/GitFolio-CLI/releases/tag/v0.5.0", http.StatusFound)
	}))
	defer srv.Close()
	defer func(u, v, b string) { latestURL, version, bundled = u, v, b }(latestURL, version, bundled)
	latestURL, version = srv.URL, "0.4.1"
	ask := func(args ...string) map[string]any {
		t.Helper()
		v, err := jsonStdout(t, func() error { return run(append([]string{"version", "--json"}, args...)) })
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := ask(); looks != 0 || v["latest"] != nil {
		t.Errorf("without --latest: %d look-ups, %v", looks, v)
	}
	if v := ask("--latest"); looks != 1 || v["latest"] != "0.5.0" || v["upToDate"] != false {
		t.Errorf("--latest: %d look-ups, %v", looks, v)
	}
	bundled = "yes"
	if v := ask("--latest"); looks != 1 || v["latest"] != nil {
		t.Errorf("--latest in an app's copy: %d look-ups, %v", looks, v)
	}
}

// The newest release is found through a renamed repository too: its old address redirects to the new
// one first, and only then to the tag.
func TestLatestReleaseRenamed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new/releases/latest", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/new/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new/releases/tag/v0.6.0", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer func(u string) { latestURL = u }(latestURL)
	latestURL = srv.URL + "/old/releases/latest"
	if tag, err := latestRelease(); err != nil || tag != "v0.6.0" {
		t.Errorf("latestRelease = %q, %v; want v0.6.0", tag, err)
	}
}

// A build from source carries the release it leads to: "0.6.0-dev" is release 0.6.0 and behaves as a
// build from source (its own data folder, no updates), as "dev" does.
func TestSourceBuildVersion(t *testing.T) {
	defer func(v string) { version = v }(version)
	for v, want := range map[string]struct {
		release string
		dev     bool
	}{"0.6.0-dev": {"0.6.0", true}, "dev": {"", true}, "0.6.0": {"0.6.0", false}} {
		version = v
		got, err := jsonStdout(t, func() error { return run([]string{"version", "--json"}) })
		if err != nil || got["release"] != want.release || got["dev"] != want.dev || sourceBuild() != want.dev || updatesItself() == want.dev {
			t.Errorf("version %q: %v %v", v, got, err)
		}
		dir, err := dataDir()
		if err != nil || strings.HasSuffix(dir, "gitfolio-dev") != want.dev {
			t.Errorf("version %q: data folder %s %v", v, dir, err)
		}
	}
}

// update --path updates another copy the way it was installed, after asking it its version (one before
// 0.6.0 prints it in its header); one built from source or shipped by an app is left as it is.
func TestUpdatePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the copies here are shell scripts")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Alineteam-Inc/GitFolio-CLI/releases/tag/v0.6.0", http.StatusFound)
	}))
	defer srv.Close()
	defer func(u string, j bool, o io.Writer) { latestURL, jsonOut, out = u, j, o }(latestURL, jsonOut, out)
	latestURL, jsonOut, out = srv.URL, true, io.Discard
	dir := t.TempDir()
	copyAt := func(rel, prints string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf '%s\\n' '"+prints+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cask := copyAt("brew/Caskroom/gitfolio/0.6.0/gitfolio", `{"version":"0.6.0","dev":false,"bundled":false}`)
	for p, want := range map[string]string{
		copyAt("bin/gitfolio", " :: GitFolio ::          (0.6.0)"): `{"from":"0.6.0","method":"script","to":"0.6.0","updated":false}`,
		cask: `{"from":"0.6.0","method":"brew","to":"0.6.0","updated":false}`,
		copyAt("src/gitfolio", `{"version":"0.6.0-dev","dev":true,"bundled":false}`): `{"from":"0.6.0-dev","method":"source","updated":false}`,
		copyAt("app/gitfolio", `{"version":"0.6.0","dev":false,"bundled":true}`):     `{"from":"0.6.0","method":"app","updated":false}`,
	} {
		v, err := jsonStdout(t, func() error { return cmdUpdate(dir, []string{"--path", p, "--json"}) })
		if b, _ := json.Marshal(v); err != nil || string(b) != want {
			t.Errorf("%s: %s %v, want %s", p, b, err, want)
		}
	}
	// A cask is upgraded with the brew of its own Homebrew, which an app's PATH lacks.
	real, _ := filepath.EvalSymlinks(dir) // a temporary folder may be behind a link (/var on macOS)
	if cmd, _ := updateCommand("v0.6.0", cask); cmd.Args[len(cmd.Args)-1] != filepath.Join(real, "brew", "bin", "brew") {
		t.Errorf("cask update: %q", cmd.Args)
	}
	for _, args := range [][]string{{"--path", filepath.Join(dir, "none")}, {"--path"}} {
		if err := cmdUpdate(dir, args); err == nil {
			t.Errorf("update %q went through", args)
		} else if code := errorCode(err); code != map[int]string{1: "usage", 2: "updatePathBad"}[len(args)] {
			t.Errorf("update %q: %s", args, code)
		}
	}
}
