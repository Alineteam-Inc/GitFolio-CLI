package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The git command history is git's own record of what gitfolio ran (GIT_TRACE), not gitfolio's account
// of it: each run that calls git adds a dated header line, then git appends one line per command.
// `gitfolio history` shows it; the oldest runs go first once the file passes its size limit.
const (
	historyFile   = "git-history.log"
	historyMax    = 1 << 20 // 1 MB
	historyHeader = "# "
)

var (
	gitTrace    string   // file git() points GIT_TRACE at; "" = no history (off, or not started by run)
	historyArgs []string // the gitfolio command, for the run's header
	historyOnce sync.Once
)

// startHistory turns the history on for this run unless the user turned it off.
func startHistory(dir string, args []string) {
	cfg, err := loadConfig(dir)
	if err != nil || cfg.GitHistoryOff {
		return
	}
	gitTrace, historyArgs = filepath.Join(dir, historyFile), args
}

// traceEnv returns the GIT_TRACE setting for a git command, writing the run's header before the first
// one. A GIT_TRACE the user set is left alone, so users can keep a trace of their own (DESIGN 7.8).
func traceEnv() []string {
	if gitTrace == "" || os.Getenv("GIT_TRACE") != "" {
		return nil
	}
	historyOnce.Do(func() {
		trimHistory(gitTrace, historyMax)
		f, err := os.OpenFile(gitTrace, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "%s%s gitfolio %s\n", historyHeader, time.Now().Format(time.RFC3339), strings.Join(historyArgs, " "))
		f.Close()
	})
	return []string{"GIT_TRACE=" + gitTrace}
}

// trimHistory drops the oldest runs once the file is over max, keeping about 3/4 of max so it is not
// rewritten on every run.
// ponytail: a git command of another gitfolio run that writes during the rewrite can lose its line.
func trimHistory(p string, max int64) {
	if st, err := os.Stat(p); err != nil || st.Size() <= max {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil || int64(len(b)) <= max { // another run trimmed it since the Stat
		return
	}
	b = b[int64(len(b))-max*3/4:]
	if i := bytes.Index(b, []byte("\n"+historyHeader)); i >= 0 {
		b = b[i+1:] // start at a run's header
	}
	writeFile(p, b) // Windows refuses while a git command has the file open; trimmed next time
}

type historyRun struct {
	At      string    `json:"at"`      // when the run started, as written: "2026-10-01T22:37:53+09:00"
	Command string    `json:"command"` // "gitfolio scan --all"
	Git     []gitCall `json:"git"`
}

type gitCall struct {
	At      string `json:"at"`      // "22:37:53.123"
	Command string `json:"command"` // "git cat-file -p HEAD:package.json"
}

// parseHistory reads the history file into runs, oldest first. Only the lines naming a git command are
// kept; the file itself has everything git wrote.
func parseHistory(b []byte) []historyRun {
	var runs []historyRun
	for _, line := range strings.Split(string(b), "\n") {
		if h, ok := strings.CutPrefix(line, historyHeader); ok {
			when, cmd, _ := strings.Cut(h, " ")
			runs = append(runs, historyRun{At: when, Command: cmd, Git: []gitCall{}})
			continue
		}
		_, git, ok := strings.Cut(line, "trace: built-in: ")
		if !ok {
			_, git, ok = strings.Cut(line, "trace: exec: ")
		}
		if !ok || len(runs) == 0 {
			continue
		}
		at, _, _ := strings.Cut(line, " ") // 22:37:53.123456
		if len(at) > 12 {
			at = at[:12]
		}
		r := &runs[len(runs)-1]
		r.Git = append(r.Git, gitCall{at, git})
	}
	return runs
}

// cmdHistory shows the git commands gitfolio ran: the last 20 runs, or all of them with --all.
func cmdHistory(dir string, args []string) error {
	jsonMode(args)
	args = withoutJSON(args)
	lang := detectLang(os.Getenv)
	if len(args) > 1 || (len(args) == 1 && args[0] != "--all") {
		return wrongWord("history", args[len(args)-1], []string{"--all", "--json"}, "gitfolio history [--all] [--json]")
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if cfg.GitHistoryOff {
		if jsonOut {
			return printJSON(map[string]any{"on": false, "runs": []historyRun{}, "total": 0})
		}
		say(lang, "historyOff")
		return nil
	}
	p := filepath.Join(dir, historyFile)
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	runs := parseHistory(b)
	shown := runs
	if len(args) == 0 {
		shown = runs[max(0, len(runs)-20):]
	}
	if jsonOut {
		return printJSON(map[string]any{"on": true, "runs": append([]historyRun{}, shown...), "total": len(runs)})
	}
	if len(runs) == 0 {
		say(lang, "historyEmpty")
		return nil
	}
	for _, r := range shown {
		when := r.At
		if t, err := time.Parse(time.RFC3339, when); err == nil {
			when = t.Format("2006-01-02 15:04:05 -07:00")
		}
		fmt.Println(when + "  " + r.Command)
		for _, g := range r.Git {
			fmt.Println("  " + g.At + "  " + g.Command)
		}
	}
	fmt.Println()
	say(lang, "historyShown", len(shown), len(runs), tildePath(p))
	return nil
}
