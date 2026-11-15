package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// bundled is set by -ldflags "-X main.bundled=yes" in a copy an app ships and updates with itself:
// such a copy never updates itself or offers to (updatesItself), as that would change the app.
var bundled string

// updatesItself reports whether this copy looks for, offers and installs newer releases: not a build
// from source, and not one an app ships.
func updatesItself() bool { return !sourceBuild() && bundled == "" }

// latestURL redirects to the newest release's tag page (no API rate limit).
var latestURL = "https://github.com/Alineteam-Inc/GitFolio-CLI/releases/latest"

const (
	updateFile  = "update.json"
	installSh   = "https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.sh"
	installPs1  = "https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.ps1"
	updateEvery = 24 * time.Hour
)

// updateState is update.json: the newest release as last looked up, and when the user was last asked
// about it at a terminal or told about it during a push.
type updateState struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest,omitempty"` // the newest release then, e.g. "v0.5.0"
	AskedAt   time.Time `json:"askedAt,omitempty"`
	NoticedAt time.Time `json:"noticedAt,omitempty"`
}

// lookUpLatest looks up the newest release at most once a day and keeps what it found. Offline or not,
// the next look is a day away.
func lookUpLatest(dir string) updateState {
	p := filepath.Join(dir, updateFile)
	var st updateState
	if loadJSON(p, &st) == nil && time.Since(st.CheckedAt) < updateEvery {
		return st
	}
	st.CheckedAt = time.Now()
	if latest, err := latestRelease(); err == nil {
		st.Latest = latest
	}
	_ = saveJSON(p, st)
	return st
}

// checkUpdate offers, at most once a day, to install a newer release the way this copy was installed
// (Homebrew, install.sh or install.ps1). It reports whether it updated, so the caller stops and the user
// runs the command again with the new binary. Builds from source never check, and nothing is asked
// unless a person is at the terminal (not hooks, scheduled runs or redirected output).
func checkUpdate(dir string) bool {
	if !updatesItself() || !interactive() || !terminal(os.Stdout) {
		return false
	}
	st := lookUpLatest(dir)
	if !newer(st.Latest, version) || time.Since(st.AskedAt) < updateEvery {
		return false
	}
	st.AskedAt = time.Now()
	_ = saveJSON(filepath.Join(dir, updateFile), st)
	lang := detectLang(os.Getenv)
	if !askYesNo(lang, "updateAsk", true, st.Latest, version) || stdinClosed {
		return false
	}
	if err := installUpdate(st.Latest, ""); err != nil {
		warn(lang, "failed", err)
		return false
	}
	say(lang, "updated", st.Latest)
	return true
}

// ttyOut opens the terminal git runs in. A hook's own output goes nowhere (the hook script discards it),
// and a GUI git client has no terminal: then it fails and nothing is shown.
var ttyOut = func() (io.WriteCloser, error) { return openTTY() }

// noticeUpdate tells the person pushing, at most once a day, that a newer release is out: someone who
// only pushes never sees the question an interactive command asks. It reads what an earlier look found,
// so a push never waits on the network (the push's background sync looks it up, lookUpLatest).
func noticeUpdate(dir string) {
	p := filepath.Join(dir, updateFile)
	var st updateState
	if !updatesItself() || loadJSON(p, &st) != nil || !newer(st.Latest, version) || time.Since(st.NoticedAt) < updateEvery {
		return
	}
	w, err := ttyOut()
	if err != nil {
		return // no terminal: the next push tries again
	}
	defer w.Close()
	// A note among git's own output: the blue ℹ in a terminal that takes color (DESIGN 7.1). The line
	// names GitFolio itself, so it needs no "GitFolio : " in front where there is no mark.
	mark := ""
	if os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && runtime.GOOS != "windows" {
		mark = "\x1b[94mℹ\x1b[0m "
	}
	fmt.Fprint(w, mark+fmt.Sprintf(tr(detectLang(os.Getenv), "updateNotice"), strings.TrimPrefix(st.Latest, "v"), version))
	st.NoticedAt = time.Now()
	_ = saveJSON(p, st)
}

// installUpdate installs release tag latest the way the copy at exe ("" for this one) was installed. What
// the installer prints goes where messages for people go (stderr with --json). With --json its last
// lines on stderr (brew's "Error: …") also end the error: a program shows the message, not stderr.
func installUpdate(latest, exe string) error {
	cmd, manual := updateCommand(latest, exe)
	var said bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, os.Stderr
	if jsonOut {
		cmd.Stderr = io.MultiWriter(os.Stderr, &said)
	}
	if err := cmd.Run(); err != nil {
		f := failure("updateFailed", err, manual).(*failErr)
		// ponytail: the last 5 lines; keep more if an installer's error runs longer.
		if lines := strings.Split(strings.TrimSpace(said.String()), "\n"); lines[0] != "" {
			f.msg += "\n\n" + strings.Join(lines[max(0, len(lines)-5):], "\n")
		}
		return f
	}
	return nil
}

// cmdUpdate checks for a newer release now, whenever the daily check last ran, and installs it the way
// the copy was installed: this one, or with --path another one (an app updating a copy installed apart
// from it, which the app's own copy may do). --json prints {updated, from, to, method}; method is brew,
// script, or one never updated here: source (built from source) or app (an app ships it).
func cmdUpdate(dir string, args []string) error {
	lang := detectLang(os.Getenv)
	exe, have, method := "", version, installMethod("", sourceBuild(), bundled != "")
	if args = withoutJSON(args); len(args) > 0 {
		if len(args) != 2 || args[0] != "--path" {
			return failure("usage", "gitfolio update [--path <gitfolio>] [--json]")
		}
		exe = args[1]
		var err error
		if have, method, err = copyOf(exe); err != nil {
			return failure("updatePathBad", exe, err)
		}
	}
	res := map[string]any{"updated": false, "from": have, "method": method}
	finish := func(key string, a ...any) error {
		if jsonOut {
			return printJSON(res)
		}
		say(lang, key, a...)
		return nil
	}
	switch method {
	case "source":
		return finish("updateFromSource")
	case "app":
		return finish("updateFromApp")
	}
	latest, err := latestRelease()
	if err != nil {
		return failure("updateCheckFailed", err)
	}
	res["to"] = strings.TrimPrefix(latest, "v")
	if exe == "" {
		now := time.Now() // looked up and asked: no offer again today
		_ = saveJSON(filepath.Join(dir, updateFile), updateState{CheckedAt: now, Latest: latest, AskedAt: now})
	}
	if !newer(latest, have) {
		return finish("updateLatest", have)
	}
	say(lang, "updating", have, latest)
	if err := installUpdate(latest, exe); err != nil {
		return err
	}
	res["updated"] = true
	return finish("updatedNow", latest)
}

// versionHeader is the version in what `gitfolio version` prints ("… :: GitFolio ::   (0.5.0)").
var versionHeader = regexp.MustCompile(`:: GitFolio ::\s+\(([^)\s]+)\)`)

// copyOf runs the gitfolio at exe for its version and the way it updates (installMethod). One before
// 0.6.0 takes no version --json and prints its header for it.
func copyOf(exe string) (have, method string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, exe, "version", "--json").Output()
	if err != nil {
		return "", "", err
	}
	var v struct {
		Version      string
		Dev, Bundled bool
	}
	if json.Unmarshal(b, &v) != nil || v.Version == "" {
		m := versionHeader.FindSubmatch(b)
		if m == nil {
			return "", "", errors.New("no GitFolio version in its output")
		}
		v.Version = string(m[1])
		v.Dev = v.Version == "dev" || strings.HasSuffix(v.Version, "-dev")
	}
	return v.Version, installMethod(exe, v.Dev, v.Bundled), nil
}

// installMethod is how the copy at exe ("" for this one) updates: brew (a Homebrew cask), script
// (install.sh or install.ps1), or never here: source or app.
func installMethod(exe string, dev, app bool) string {
	sep := string(filepath.Separator)
	switch {
	case dev:
		return "source"
	case app:
		return "app"
	case strings.Contains(realExe(exe), sep+"Caskroom"+sep):
		return "brew"
	}
	return "script"
}

// realExe is the file the gitfolio at exe ("" for this one) is, links followed; "" when unknown.
func realExe(exe string) string {
	if exe == "" {
		exe, _ = os.Executable()
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// latestRelease returns the newest release tag, e.g. "v0.2.0": the tag page releases/latest redirects
// to. Redirects before it are followed: a renamed repository redirects its old address first.
func latestRelease() (string, error) {
	c := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if strings.Contains(req.URL.Path, "/releases/tag/") {
			return http.ErrUseLastResponse // its address names the tag; the page itself is not needed
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	res, err := c.Head(latestURL)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	tag := path.Base(res.Header.Get("Location")) // …/releases/tag/v0.2.0
	if !strings.HasPrefix(tag, "v") {
		return "", errors.New("no release")
	}
	return tag, nil
}

// newer reports whether release tag a ("v1.2.3") is newer than version b ("1.2.3"). Anything that is
// not three numbers counts as not newer.
func newer(a, b string) bool {
	parse := func(s string) (n [3]int, ok bool) {
		parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
		if len(parts) != 3 {
			return n, false
		}
		for i, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil {
				return n, false
			}
			n[i] = v
		}
		return n, true
	}
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// updateCommand returns the command that installs release tag the way the binary at exe ("" for this
// one) was installed, and the same thing written out for the user in case it fails.
func updateCommand(tag, exe string) (*exec.Cmd, string) {
	exe = realExe(exe)
	sep := string(filepath.Separator)
	switch {
	case strings.Contains(exe, sep+"Caskroom"+sep): // Homebrew cask, macOS or Linux
		// The brew of that Homebrew: an app's PATH has none. Only GitFolio's tap is brought up to date
		// (brew update fetches every tap and Homebrew itself, which takes minutes); brew update if it fails.
		brew := filepath.Join(exe[:strings.Index(exe, sep+"Caskroom"+sep)], "bin", "brew")
		// As brew update runs git on a tap: no hooks, no prompt; fetch and merge, whatever pull.rebase says.
		s := `{ t=$("$0" --repository alineteam-inc/tap) && export GIT_TERMINAL_PROMPT=0 &&
git -C "$t" -c core.hooksPath=/dev/null fetch -q && git -C "$t" -c core.hooksPath=/dev/null merge -q --ff-only '@{u}' ||
"$0" update; } && HOMEBREW_NO_AUTO_UPDATE=1 "$0" upgrade --cask alineteam-inc/tap/gitfolio`
		// The full name, here and by hand: Homebrew 7 loads a cask of a tap not trusted (brew trust) only
		// when named so.
		return exec.Command("sh", "-c", s, brew), "brew update && brew upgrade --cask alineteam-inc/tap/gitfolio"
	case runtime.GOOS == "windows":
		s := "irm " + installPs1 + " | iex"
		c := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", s)
		c.Env = installEnv(tag, exe)
		return c, s
	default:
		s := "curl -fsSL " + installSh + " | sh"
		c := exec.Command("sh", "-c", s)
		c.Env = installEnv(tag, exe)
		return c, s
	}
}

// installEnv tells the install script the version and to replace this copy where it is. When where is
// unknown (exe ""), the script uses its own default folder, never the current one.
func installEnv(tag, exe string) []string {
	env := append(os.Environ(), "GITFOLIO_VERSION="+tag)
	if exe != "" {
		env = append(env, "GITFOLIO_INSTALL_DIR="+filepath.Dir(exe))
	}
	return env
}

// terminal reports whether f is a terminal: a character device other than the null device, which is
// one too (`gitfolio email add x </dev/null` must not ask for a code).
func terminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(st, null)
}
