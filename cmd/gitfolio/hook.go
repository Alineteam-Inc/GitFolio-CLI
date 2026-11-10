package main

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const hookMarker = "# gitfolio-managed"

// hookFind locates gitfolio even when a GUI git client runs hooks with a short PATH.
const hookFind = `g=$(command -v gitfolio 2>/dev/null)
for p in /opt/homebrew/bin/gitfolio /usr/local/bin/gitfolio "$HOME/.local/bin/gitfolio" "$HOME/go/bin/gitfolio"; do
	[ -z "$g" ] && [ -x "$p" ] && g=$p
done
`

// Hook scripts never fail and never wait: git keeps working when gitfolio is missing or broken.
// A hook that existed before is renamed to <name>.gitfolio-orig and still runs first;
// for pre-push its exit code is honored, so it can still block a push.
var hookScripts = map[string]string{
	"post-commit": "#!/bin/sh\n" + hookMarker + ` (github.com/Alineteam-Inc/GitFolio-CLI): notes AI agent use for this commit
d=$(dirname "$0")
[ -x "$d/post-commit.gitfolio-orig" ] && "$d/post-commit.gitfolio-orig" "$@"
` + hookFind + `[ -n "$g" ] && "$g" hook post-commit >/dev/null 2>&1
exit 0
`,
	"pre-push": "#!/bin/sh\n" + hookMarker + ` (github.com/Alineteam-Inc/GitFolio-CLI): collects pushed commits in the background
d=$(dirname "$0")
in=$(cat)
if [ -x "$d/pre-push.gitfolio-orig" ]; then
	printf '%s\n' "$in" | "$d/pre-push.gitfolio-orig" "$@" || exit $?
fi
` + hookFind + `[ -n "$g" ] && "$g" hook pre-push "$PPID" >/dev/null 2>&1
exit 0
`,
}

// hooksDir returns the directory git runs repo's hooks from, and whether it is husky's: husky 9 sets
// core.hooksPath to .husky/_, a folder it generates on this computer and keeps out of git. Other
// directories outside .git (a shared core.hooksPath, older husky) are shared with other repositories
// or committed with the project, so they are refused.
func hooksDir(repo string) (dir string, husky bool, err error) {
	out, err := git(repo, "rev-parse", "--git-path", "hooks", "--git-common-dir")
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return "", false, fmt.Errorf("unexpected git rev-parse output %q", out)
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(repo, p)
		}
		return filepath.Clean(p)
	}
	hooks, common := abs(lines[0]), abs(lines[1])
	if rel, err := filepath.Rel(common, hooks); err != nil || strings.HasPrefix(rel, "..") {
		if _, err := os.Stat(filepath.Join(hooks, "h")); err == nil && filepath.Base(hooks) == "_" {
			return hooks, true, nil
		}
		return "", false, failure("hooksElsewhere", tildePath(hooks))
	}
	return hooks, false, nil
}

// huskyHooks puts gitfolio's lines at the top of husky's generated pre-push and post-commit files, or
// takes them out. They go first because husky's own line ends the script. husky writes these files
// again on npm install, so scans put the lines back.
func huskyHooks(dir string, install bool) error {
	for name, cmd := range map[string]string{"pre-push": `hook pre-push "$PPID"`, "post-commit": "hook post-commit"} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s := string(b)
		if i := strings.Index(s, hookMarker); i >= 0 {
			if install {
				continue // already there
			}
			if j := strings.Index(s, hookMarker+" end\n"); j > i {
				s = s[:i] + s[j+len(hookMarker+" end\n"):]
			}
		} else if install {
			first, rest, _ := strings.Cut(cmp.Or(s, "#!/usr/bin/env sh\n"), "\n")
			s = first + "\n" + hookMarker + " (github.com/Alineteam-Inc/GitFolio-CLI)\n" + hookFind +
				`[ -n "$g" ] && "$g" ` + cmd + " </dev/null >/dev/null 2>&1\n" + hookMarker + " end\n" + rest
		} else {
			continue
		}
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func installHooks(repo string) error {
	dir, husky, err := hooksDir(repo)
	if err != nil {
		return err
	}
	if husky {
		return huskyHooks(dir, true)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, script := range hookScripts {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err == nil {
			if strings.Contains(string(b), hookMarker) {
				continue // already installed
			}
			if _, err := os.Stat(p + ".gitfolio-orig"); err == nil {
				return failure("hooksBothExist", tildePath(p))
			}
			if err := os.Rename(p, p+".gitfolio-orig"); err != nil {
				return err
			}
		}
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// uninstallHooks removes gitfolio's hooks and puts back the ones that were there before.
func uninstallHooks(repo string) error {
	dir, husky, err := hooksDir(repo)
	if err != nil {
		return err
	}
	if husky {
		return huskyHooks(dir, false)
	}
	for name := range hookScripts {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), hookMarker) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		if _, err := os.Stat(p + ".gitfolio-orig"); err == nil {
			if err := os.Rename(p+".gitfolio-orig", p); err != nil {
				return err
			}
		}
	}
	return nil
}

// hookStatus reports whether gitfolio's pre-push hook is installed in repo, for `gitfolio list`.
func hookStatus(repo string) string {
	dir, _, err := hooksDir(repo)
	if err != nil {
		return "manual"
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pre-push")); err == nil && strings.Contains(string(b), hookMarker) {
		return "on"
	}
	return "off"
}

// cmdHook runs inside git hooks. Hooks ignore its exit code, so errors only matter for debugging.
func cmdHook(dir string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gitfolio hook post-commit|pre-push")
	}
	switch args[0] {
	case "post-commit":
		agents := agentsFromEnv(os.Getenv)
		if len(agents) == 0 {
			return nil
		}
		out, err := git(".", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		return appendAgentTag(dir, agentTag{Hash: strings.TrimSpace(out), Agents: agents})
	case "pre-push":
		// git waits for this hook, so the work runs in a separate process that outlives it.
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		arg := ""
		if len(args) > 1 {
			arg = args[1]
		}
		wait := strconv.Itoa(pushPID(arg))
		if wait == "0" {
			// The push process can't be found: Git for Windows runs husky's `#!/usr/bin/env sh` stubs
			// through env, whose exec leaves no parent link. Wait for the push to update the refs instead.
			wait = "refs:" + remoteRefs(".")
		}
		noticeUpdate(dir)
		return exec.Command(exe, "hook", "push-wait", wait).Start()
	case "push-wait":
		signal.Ignore(syscall.SIGHUP) // keep going if the terminal closes right after the push
		if len(args) > 1 {
			if refs, ok := strings.CutPrefix(args[1], "refs:"); ok {
				waitRefs(refs, 10*time.Minute)
			} else if pid, err := strconv.Atoi(args[1]); err == nil {
				waitExit(pid, 10*time.Minute)
			}
		}
		top, err := topLevel(".")
		if err != nil {
			return err
		}
		if updatesItself() { // what the next push tells about a newer release (noticeUpdate), outside the lock
			defer lookUpLatest(dir)
		}
		// Only commits on remote-tracking refs are sent (pending), so a rejected push adds nothing.
		return withLock(dir, func() error {
			repos, err := loadRepos(dir)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(repos, func(r Repo) bool { return r.Path == top })
			if main, ok := mainWorktree("."); i < 0 && ok { // pushed from a linked worktree of a registered one
				i = slices.IndexFunc(repos, func(r Repo) bool { return filepath.Clean(r.Path) == main })
			}
			if i < 0 {
				return nil
			}
			if _, err := scanRepo(dir, &repos[i], false); err != nil {
				return err
			}
			if err := saveRepos(dir, repos); err != nil {
				return err
			}
			cfg, err := loadConfig(dir)
			if err != nil || cfg.AutoSyncOff {
				return err
			}
			_, err = syncData(dir, false) // offline or failed: sent by the next push or sync
			return err
		})
	}
	return fmt.Errorf("unknown hook %q", args[0])
}

// remoteRefs fingerprints the repository's remote-tracking refs, which git updates when a push succeeds.
func remoteRefs(repo string) string {
	out, _ := git(repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/remotes")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(out)))[:16]
}

// waitRefs waits until the remote-tracking refs differ from before and have stopped changing, at most
// max. A push that is rejected or updates no remote-tracking ref waits the whole time; the scan after
// it then finds nothing new.
func waitRefs(before string, max time.Duration) {
	last := before
	for deadline := time.Now().Add(max); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		now := remoteRefs(".")
		if now != before && now == last {
			return
		}
		last = now
	}
}

// withLock runs fn while holding gitfolio's data lock, so hooks and commands never write at the same time.
// The lock is the system's own lock on lock.os (flock, LockFileEx), which goes with its process: a run
// that crashed or slept with the computer leaves nothing to clean up, and no run opens a file that
// another removes. Releases before 0.6.4 lock by creating the file lock instead, so a run makes that
// one too while it holds lock.os.
func withLock(dir string, fn func() error) error {
	deadline := time.Now().Add(30 * time.Second)
	held, err := lockFile(filepath.Join(dir, "lock.os"))
	for errors.Is(err, errLocked) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		held, err = lockFile(filepath.Join(dir, "lock.os"))
	}
	if errors.Is(err, errLocked) {
		return failure("busy")
	}
	if err != nil {
		return err
	}
	defer held.Close()
	p := filepath.Join(dir, "lock")
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.WriteString(lockMark)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			defer os.Remove(p)
			if err != nil {
				return err
			}
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		// Looked at, never opened: an older release may be removing it. While this run holds lock.os, a
		// lock with lockMark is one a run of this release left behind; any other is an older release's,
		// left behind after 10 minutes as there.
		if st, err := os.Stat(p); err == nil && (st.Size() == int64(len(lockMark)) || time.Since(st.ModTime()) > 10*time.Minute) {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return failure("busy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockMark is what a run that also holds lock.os writes into the file lock: no older release writes
// one this long (0.6.3 writes its process ID).
const lockMark = "held with lock.os by gitfolio\n"

// errLocked is lockFile's answer while another run holds the lock.
var errLocked = errors.New("locked by another run")
