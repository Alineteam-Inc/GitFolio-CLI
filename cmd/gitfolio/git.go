package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// logFormat separates commits with \x1e and fields with \x1f; numstat lines follow the last \x1f.
const logFormat = "--format=%x1e%H%x1f%aI%x1f%aN%x1f%aE%x1f%B%x1f"

// gitPath is the git that git() runs: the one on the PATH, or the Command Line Tools' own (gitWorks).
var gitPath = "git"

// git runs git inside dir. GIT_DIR and friends are dropped so that running
// from inside a git hook does not redirect the command to the hook's repository.
func git(dir string, args ...string) (string, error) { return gitStdin(dir, "", args...) }

// gitStdin is git with in on its standard input.
func gitStdin(dir, in string, args ...string) (string, error) {
	cmd := exec.Command(gitPath, append([]string{"-C", dir}, args...)...)
	if in != "" {
		cmd.Stdin = strings.NewReader(in)
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") && !strings.HasPrefix(kv, "GIT_WORK_TREE=") && !strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, traceEnv()...) // git command history (history.go)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// gitWorks reports whether git can run, and puts it on this run's PATH when it is installed outside
// it (gitDirs). On macOS /usr/bin/git is there without git too: it stands in for the Command Line Tools,
// and only counts once they are installed. Asking xcode-select, not running it, so Apple's install
// window does not open by itself. The tools' git then runs directly: /usr/bin/git looks it up on every
// run, which doubles the time of each git command.
func gitWorks() bool {
	p, err := exec.LookPath("git")
	if err == nil && (runtime.GOOS != "darwin" || p != "/usr/bin/git") {
		return true
	}
	if err == nil {
		if out, err := exec.Command("xcode-select", "-p").Output(); err == nil {
			if real := filepath.Join(strings.TrimSpace(string(out)), "usr", "bin", "git"); isFile(real) {
				gitPath = real
			}
			return true
		}
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, d := range gitDirs() {
		if isFile(filepath.Join(d, name)) {
			return os.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH")) == nil
		}
	}
	return false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// gitDirs are where git is installed but may be off the PATH: an app started from Finder gets only
// /usr/bin:/bin:/usr/sbin:/sbin, and a terminal open while git was installed on Windows has the old PATH.
var gitDirs = func() []string {
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(os.Getenv("ProgramFiles"), "Git", "cmd")}
	}
	return []string{"/opt/homebrew/bin", "/usr/local/bin"}
}

// gitDownload is git's own download page for this system, for installing it by hand.
func gitDownload() string {
	switch runtime.GOOS {
	case "darwin":
		return "https://git-scm.com/download/mac"
	case "windows":
		return "https://git-scm.com/download/win"
	}
	return "https://git-scm.com/download/linux"
}

// cmdGit tells whether git is there (path and version, with --latest also how it compares to git's
// newest release), or with install starts installing it: at the terminal as init does, after asking;
// for a program (--json) without asking, the way it can start without a terminal (gitInstallStart).
func cmdGit(args []string) error {
	jsonMode(args)
	args = withoutJSON(args)
	latest := slices.Contains(args, "--latest")
	args = slices.DeleteFunc(args, func(a string) bool { return a == "--latest" })
	lang := detectLang(os.Getenv)
	switch {
	case len(args) == 0:
		found := gitWorks()
		path, version := "", ""
		if found {
			path, _ = exec.LookPath("git")
			if out, err := exec.Command("git", "--version").Output(); err == nil { // "git version 2.47.0"
				version = strings.TrimPrefix(strings.TrimSpace(string(out)), "git version ")
			}
		}
		if jsonOut {
			res := map[string]any{"found": found, "path": path, "version": version}
			if latest && found {
				if newest, err := gitUpstream(); err == nil { // offline: left out
					res["latest"] = strings.TrimPrefix(newest, "v")
					if mine := gitRelease(version); mine != "" {
						res["upToDate"] = !newer(newest, mine)
						if newer(newest, mine) {
							res["updateHint"] = gitUpdateHint(lang, path)
						}
					}
				}
			}
			return printJSON(res)
		}
		if !found {
			say(lang, "gitMissing")
			return nil
		}
		say(lang, "gitFound", version, path)
		return nil
	case len(args) == 1 && args[0] == "install":
		if gitWorks() {
			if jsonOut {
				return printJSON(map[string]any{"started": false, "how": "installed"})
			}
			say(lang, "gitInstalled")
			return nil
		}
		if jsonOut {
			return gitInstallStart(lang)
		}
		if !interactive() || !installGit(lang) {
			return failure("noGit")
		}
		return nil
	case len(args) == 1 && args[0] == "update":
		if !gitWorks() {
			return failure("noGit")
		}
		path, _ := exec.LookPath("git")
		return gitUpdate(lang, path)
	}
	return failure("usage", "gitfolio git [install | update] [--json]")
}

// gitUpdate updates git as gitUpdater says. A program (--json) has asked its user: brew and winget run
// and are waited for, a window opens and it returns at once. At the terminal it asks first; elsewhere,
// and where it cannot update, it says how.
func gitUpdate(lang, path string) error {
	args, how := gitUpdater(runtime.GOOS, path, gitPath, func(n string) bool { _, err := exec.LookPath(n); return err == nil })
	if args == nil || !jsonOut && !interactive() {
		if jsonOut {
			return printJSON(map[string]any{"started": false, "how": how, "message": gitUpdateHint(lang, path), "url": gitDownload()})
		}
		notice(gitUpdateHint(lang, path))
		return nil
	}
	if !jsonOut && args[0] != "open" && (!askYesNo(lang, "gitUpdateAsk", true, strings.Join(args, " ")) || stdinClosed) {
		return nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = errOut, errOut // stdout carries the result alone
	if !jsonOut {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if how == "winget" && errors.As(err, &exit) && exit.ExitCode() == 0x8A15002B { // no newer Git for Windows yet
			if jsonOut {
				return printJSON(map[string]any{"started": false, "how": how, "message": strings.TrimSpace(tr(lang, "gitUpdateNone"))})
			}
			say(lang, "gitUpdateNone")
			return nil
		}
		return failure("gitUpdateFailed", err)
	}
	switch {
	case jsonOut:
		return printJSON(map[string]any{"started": true, "how": how})
	case args[0] == "open":
		say(lang, "gitUpdateOpened")
		return nil
	}
	return cmdGit(nil) // the version now
}

// gitUpdater is the command that updates the git at path the way it was installed, and how: brew for
// Homebrew's (on Linux too), winget for Git for Windows, and for Apple's the window its updates come in,
// Software Update for the Command Line Tools or the App Store for Xcode's (tools: the git gitWorks runs).
// nil and "manual" where it takes a terminal (sudo for a Linux distribution's) or is not known.
func gitUpdater(goos, path, tools string, has func(string) bool) ([]string, string) {
	sep := string(filepath.Separator)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		if i := strings.Index(real, sep+"Cellar"+sep+"git"+sep); i >= 0 {
			return []string{filepath.Join(real[:i], "bin", "brew"), "upgrade", "git"}, "brew"
		}
	}
	switch {
	case goos == "darwin" && path == "/usr/bin/git" && strings.Contains(tools, ".app/"):
		return []string{"open", "macappstore://showUpdatesPage"}, "appStore"
	case goos == "darwin" && path == "/usr/bin/git":
		return []string{"open", "x-apple.systempreferences:com.apple.Software-Update-Settings.extension"}, "softwareUpdate"
	case goos == "windows" && has("winget"):
		return []string{"winget", "upgrade", "--id", "Git.Git", "-e", "--source", "winget",
			"--accept-package-agreements", "--accept-source-agreements"}, "winget"
	}
	return nil, "manual"
}

// gitUpstream is git's newest release ("v2.55.0"), from git's own repository. A vendor's git (Apple's,
// a Linux distribution's) can be older and still carry the security fixes: gitUpdateHint says so.
func gitUpstream() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", "https://github.com/git/git", "v*")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	if tag := newestRelease(string(out)); tag != "" {
		return tag, nil
	}
	return "", errors.New("no git release found")
}

// newestRelease is the newest vX.Y.Z tag in git ls-remote output; release candidates do not count.
func newestRelease(lsRemote string) string {
	best := ""
	for _, line := range strings.Split(lsRemote, "\n") {
		_, ref, _ := strings.Cut(line, "\t")
		if tag := strings.TrimPrefix(ref, "refs/tags/"); releaseTag.MatchString(tag) && (best == "" || newer(tag, best)) {
			best = tag
		}
	}
	return best
}

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// gitRelease is the upstream release a git version is built on: "2.54.0 (Apple Git-157)" and
// "2.47.0.windows.2" are v2.54.0 and v2.47.0; "" when it is not one.
func gitRelease(version string) string {
	v, _, _ := strings.Cut(version, " ")
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return ""
	}
	return "v" + strings.Join(parts[:3], ".")
}

// gitUpdateHint says how git is updated where it is, without commands: Apple's with macOS's updates,
// others the way they were installed.
func gitUpdateHint(lang, path string) string {
	key := "gitUpdateLinux"
	switch {
	case runtime.GOOS == "darwin" && path == "/usr/bin/git":
		key = "gitUpdateApple"
	case runtime.GOOS == "darwin":
		key = "gitUpdateMac"
	case runtime.GOOS == "windows":
		key = "gitUpdateWindows"
	}
	return strings.TrimSpace(tr(lang, key))
}

// gitInstallStart starts installing git for a program, asking nothing: Apple's install window on macOS
// (it returns at once; the program checks again once the user is done), winget on Windows (it waits for
// the install and may ask for administrator rights). Where that needs a terminal for sudo or there is
// no installer, it gives the manual way: a message and git's download page.
func gitInstallStart(lang string) error {
	var args []string
	how := "manual"
	switch runtime.GOOS {
	case "darwin":
		args, how = []string{"xcode-select", "--install"}, "xcodeSelect"
	case "windows":
		if args = gitInstaller("windows", func(n string) bool { _, err := exec.LookPath(n); return err == nil }, false); args != nil {
			args, how = append(args, "--accept-package-agreements", "--accept-source-agreements"), "winget"
		}
	}
	if args == nil {
		return printJSON(map[string]any{"started": false, "how": how, "message": strings.TrimSpace(tr(lang, "gitInstallManual")), "url": gitDownload()})
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = errOut, errOut // stdout carries the result alone
	if err := cmd.Run(); err != nil {
		return failure("gitInstallFailed", err)
	}
	return printJSON(map[string]any{"started": true, "how": how, "url": gitDownload()})
}

// gitInstaller is the command that installs git with the system's own installer, nil when there is
// none to use: the Command Line Tools on macOS, winget on Windows, the package manager on Linux (through
// sudo unless root). has reports whether a command is on the PATH.
func gitInstaller(goos string, has func(string) bool, root bool) []string {
	switch goos {
	case "darwin":
		return []string{"xcode-select", "--install"}
	case "windows":
		if has("winget") {
			return []string{"winget", "install", "--id", "Git.Git", "-e", "--source", "winget"}
		}
		return nil
	}
	var install []string
	switch {
	case has("apt-get"):
		install = []string{"sh", "-c", "apt-get update && apt-get install -y git"}
	case has("dnf"):
		install = []string{"dnf", "install", "-y", "git"}
	case has("yum"):
		install = []string{"yum", "install", "-y", "git"}
	case has("pacman"):
		install = []string{"pacman", "-S", "--noconfirm", "git"}
	case has("zypper"):
		install = []string{"zypper", "--non-interactive", "install", "git"}
	case has("apk"):
		install = []string{"apk", "add", "git"}
	default:
		return nil
	}
	if root {
		return install
	}
	if !has("sudo") {
		return nil
	}
	return append([]string{"sudo"}, install...)
}

// installGit offers, at the terminal, to install git with gitInstaller, and reports whether git runs
// after it. Nothing is installed without a yes.
func installGit(lang string) bool {
	warn(lang, "gitMissing")
	args := gitInstaller(runtime.GOOS, func(name string) bool { _, err := exec.LookPath(name); return err == nil }, os.Geteuid() == 0)
	if args == nil {
		notice(tr(lang, "gitInstallManual"))
		return false
	}
	shown := make([]string, len(args))
	for i, a := range args {
		if shown[i] = a; strings.ContainsAny(a, " &") {
			shown[i] = shellQuote(a)
		}
	}
	if !askYesNo(lang, "gitInstallAsk", true, strings.Join(shown, " ")) || stdinClosed {
		return false
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		warn(lang, "gitInstallFailed", err)
		notice(tr(lang, "gitInstallManual"))
		return false
	}
	if !gitWorks() { // macOS installs in its own window, after this returns
		notice(tr(lang, "gitInstallPending"))
		return false
	}
	say(lang, "gitInstalled")
	return true
}

func topLevel(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if abs, aerr := filepath.Abs(dir); aerr == nil {
			dir = abs
		}
		return "", failure("notGitRepo", tildePath(dir))
	}
	return strings.TrimSpace(out), nil
}

// mainWorktree is the main working tree of dir when dir is a linked one (git worktree add), whose pushes
// run the main one's hooks: only there does git name the shared .git folder by an absolute path.
func mainWorktree(dir string) (string, bool) {
	out, err := git(dir, "rev-parse", "--git-common-dir")
	common := strings.TrimSpace(out)
	if err != nil || !filepath.IsAbs(common) {
		return "", false
	}
	return filepath.Dir(common), true
}

// providers maps git service hosts to the provider sent to aline.team; any other host is OTHER
// (self-hosted servers included), so internal host names never leave the machine.
var providers = map[string]string{
	"github.com":        "GITHUB",
	"ssh.github.com":    "GITHUB",
	"gitlab.com":        "GITLAB",
	"bitbucket.org":     "BITBUCKET",
	"dev.azure.com":     "DEVOPS",
	"ssh.dev.azure.com": "DEVOPS",
}

// remote returns the repository's provider and owner/repo on its git service (origin first,
// otherwise the first remote). aline.team resolves them to the service's repository ID.
// It is where commits are pushed: the push URL when it differs from the fetch URL (fetch from GitLab,
// push to GitHub), the first of several, unless that is not a git service (a local mirror): then the
// fetch URL. Both are empty without such a remote.
func remote(repo string) (provider, namespace string) {
	name := "origin"
	if _, err := git(repo, "remote", "get-url", name); err != nil {
		names, err := git(repo, "remote")
		if err != nil || len(strings.Fields(names)) == 0 {
			return "", ""
		}
		name = strings.Fields(names)[0]
	}
	for _, args := range [][]string{{"remote", "get-url", "--push", name}, {"remote", "get-url", name}} {
		if out, err := git(repo, args...); err == nil {
			first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
			if provider, namespace = parseRemote(strings.TrimSpace(first)); provider != "" {
				return provider, namespace
			}
		}
	}
	return "", ""
}

// parseRemote extracts the provider and owner/repo from https, ssh and scp-style
// ("git@host:owner/repo.git") remote URLs. Host, credentials (https://user:token@host/...)
// and port are never returned.
func parseRemote(raw string) (provider, namespace string) {
	var host, p string
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host, p = u.Hostname(), u.Path
	} else if !strings.Contains(raw, "://") {
		if i := strings.Index(raw, ":"); i > 0 {
			host, p = raw[:i], raw[i+1:]
			if j := strings.LastIndex(host, "@"); j >= 0 {
				host = host[j+1:]
			}
		}
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if !strings.Contains(host, ".") || p == "" { // local paths such as /srv/repo.git or C:\repo
		return "", ""
	}
	host = strings.ToLower(host)
	provider = providers[host]
	if strings.HasSuffix(host, ".visualstudio.com") {
		provider = "DEVOPS"
	}
	if provider == "" {
		provider = "OTHER"
	}
	if provider == "DEVOPS" {
		p = devopsNamespace(host, p)
	}
	return provider, p
}

// devopsNamespace turns an Azure DevOps remote path into organization/project/repository, the form
// aline.team builds https://dev.azure.com/org/project/_git/repo from. It takes dev.azure.com/org/project/_git/repo,
// ssh.dev.azure.com:v3/org/project/repo and the older org.visualstudio.com/[DefaultCollection/]project/_git/repo.
func devopsNamespace(host, p string) string {
	if u, err := url.PathUnescape(p); err == nil { // scp-style paths are still escaped ("My%20Project")
		p = u
	}
	parts := strings.Split(strings.TrimPrefix(p, "v3/"), "/")
	if org, ok := strings.CutSuffix(host, ".visualstudio.com"); ok && org != "vs-ssh" {
		parts = append([]string{org}, parts...)
		if len(parts) > 1 && parts[1] == "DefaultCollection" {
			parts = slices.Delete(parts, 1, 2)
		}
	}
	if i := slices.Index(parts, "_git"); i == 1 && len(parts) == 3 {
		parts[1] = parts[2] // org/_git/repo: the project has the repository's name
	} else if i >= 0 {
		parts = slices.Delete(parts, i, i+1)
	}
	return strings.Join(parts, "/")
}

// myEmails returns the lowercased emails that identify the user's own commits in repo:
// the work emails registered with `gitfolio email` and the repository's git config user.email.
func myEmails(repo string, work []string) (map[string]bool, error) {
	mine := map[string]bool{}
	for _, e := range work {
		mine[strings.ToLower(e)] = true
	}
	if out, err := git(repo, "config", "user.email"); err == nil {
		if e := strings.ToLower(strings.TrimSpace(out)); e != "" {
			mine[e] = true
		}
	}
	if len(mine) == 0 {
		return nil, failure("noUserEmail")
	}
	return mine, nil
}

// logRange picks what to read: pushed commits when the repository has remotes, otherwise HEAD.
// It returns nil when the repository has no commits yet.
func logRange(repo string) ([]string, error) {
	if _, err := git(repo, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return nil, nil
	}
	out, err := git(repo, "for-each-ref", "--count=1", "refs/remotes")
	if err != nil {
		return nil, err
	}
	rng := []string{"HEAD"}
	if strings.TrimSpace(out) != "" {
		rng = []string{"--remotes"}
	}
	// A shallow clone's boundary commits have no parents, so git would count every file as theirs: they
	// wait for fetch --unshallow.
	if p, err := git(repo, "rev-parse", "--git-path", "shallow"); err == nil {
		if p = strings.TrimSpace(p); !filepath.IsAbs(p) {
			p = filepath.Join(repo, p)
		}
		if b, err := os.ReadFile(p); err == nil && len(strings.Fields(string(b))) > 0 {
			rng = append(append(rng, "--not"), strings.Fields(string(b))...)
		}
	}
	return rng, nil
}

// remoteBranches maps every commit on a remote-tracking branch to one branch name ("main",
// "feature/login"). The remote's default branch comes first, so a commit already merged there is
// reported on it, as the aline.team GitHub sync sees it; other commits get the branch they were pushed to.
func remoteBranches(repo string) (map[string]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, ref := range strings.Fields(out) {
		if !strings.HasSuffix(ref, "/HEAD") {
			refs = append(refs, ref)
		}
	}
	if head, err := git(repo, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if i := slices.Index(refs, strings.TrimSpace(head)); i > 0 {
			refs = append(append([]string{refs[i]}, refs[:i]...), refs[i+1:]...)
		}
	}
	branch := map[string]string{}
	var done []string
	for _, ref := range refs {
		out, err := git(repo, append([]string{"rev-list", "--no-merges", ref, "--not"}, done...)...)
		if err != nil {
			return nil, err
		}
		_, name, _ := strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/") // drop the remote name
		for _, h := range strings.Fields(out) {
			branch[h] = name
		}
		done = append(done, ref)
	}
	return branch, nil
}

// gitLog reads rng's commits; with files, also the files each changed, by name only: counting the
// changed lines of everyone's whole history takes most of the time, and only the commits stored need
// them (fileStats).
func gitLog(repo string, rng []string, files bool) (string, error) {
	args := []string{"-c", "core.quotePath=false", "log", "--no-merges", "--no-color", "--no-show-signature", logFormat}
	if files {
		args = append(args, "-z", "-M", "--name-status")
	}
	return git(repo, append(args, rng...)...)
}

// fileStats reads the files of the commits hashes with their changed lines, by commit hash.
func fileStats(repo string, hashes []string) (map[string][]FileStat, error) {
	stats := map[string][]FileStat{}
	if len(hashes) == 0 {
		return stats, nil
	}
	// The hashes go on stdin: a command line has a length limit (Windows: 32767 characters).
	args := []string{"-c", "core.quotePath=false", "log", "--no-walk=unsorted", "--stdin", "--no-color", "--no-show-signature", "-z", "-M", "--numstat", "--summary", logFormat}
	out, err := gitStdin(repo, strings.Join(hashes, "\n")+"\n", args...)
	if err != nil {
		return nil, err
	}
	for _, c := range parseLog(out) {
		stats[c.Hash] = c.Files
	}
	for _, h := range hashes {
		if _, ok := stats[h]; !ok {
			return nil, fmt.Errorf("git log: no changes read for %s", h)
		}
	}
	return stats, nil
}

func parseLog(out string) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(rec, "\x1f", 6)
		if len(f) < 6 {
			continue
		}
		c := Commit{Hash: f[0], Date: f[1], AuthorEmail: f[3], Message: strings.TrimSpace(f[4])}
		c.AIAgents = detectAgents(f[2], c.AuthorEmail, c.Message)
		c.coAuthors = coAuthorEmails(c.Message)
		created := map[string]bool{}
		// -z: a file is "<added>\t<deleted>\t<path>" (--numstat) or "<status>", "<path>" (--name-status), the
		// fields ending in NUL and the paths as they are; a rename has its old and new path in two fields of
		// their own. The --summary lines follow, quoted where needed.
		fields := strings.Split(strings.TrimPrefix(f[5], "\x00\n"), "\x00")
		for i := 0; i < len(fields); i++ {
			v := fields[i]
			if n := strings.SplitN(v, "\t", 3); len(n) == 3 && isCount(n[0]) && isCount(n[1]) {
				add, _ := strconv.Atoi(n[0]) // binary files report "-", counted as 0
				del, _ := strconv.Atoi(n[1])
				name := n[2]
				if name == "" && i+2 < len(fields) { // a rename: the new path
					name, i = fields[i+2], i+2
				}
				c.Files = append(c.Files, FileStat{Name: name, Add: add, Del: del})
				continue
			}
			if isStatus(v) {
				skip := 1
				if v[0] == 'R' || v[0] == 'C' { // renamed or copied: old path, new path
					skip = 2
				}
				if i+skip >= len(fields) {
					break
				}
				name := fields[i+skip]
				if v == "A" {
					created[name] = true
				}
				c.Files = append(c.Files, FileStat{Name: name})
				i += skip
				continue
			}
			for _, line := range strings.Split(v, "\n") {
				if rest, ok := strings.CutPrefix(line, " create mode "); ok { // " create mode 100644 <path>"
					if _, p, ok := strings.Cut(rest, " "); ok {
						if u, err := strconv.Unquote(p); err == nil && strings.HasPrefix(p, `"`) {
							p = u
						}
						created[p] = true
					}
				}
			}
		}
		for i := range c.Files { // --summary lines follow the numstat lines
			c.Files[i].Created = created[c.Files[i].Name]
		}
		commits = append(commits, c)
	}
	return commits
}

// isCount reports whether s is a --numstat line count: digits, or "-" for a binary file.
func isCount(s string) bool { return s == "-" || s != "" && strings.Trim(s, "0123456789") == "" }

// isStatus reports whether s is a --name-status status: a letter, with a score after R and C ("R087").
func isStatus(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z' && strings.Trim(s[1:], "0123456789") == ""
}
