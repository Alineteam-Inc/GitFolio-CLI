package main

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseHHMM(t *testing.T) {
	for in, ok := range map[string]bool{"09:00": true, "0:05": true, "23:59": true, "24:00": false, "9:5": false, "09:60": false, "0900": false, "": false, "ab:cd": false} {
		if _, _, got := parseHHMM(in); got != ok {
			t.Errorf("parseHHMM(%q) ok = %v, want %v", in, got, ok)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	p := launchdPlist("/Users/a&b/bin/gitfolio", "/tmp/log <1>", [][2]string{{"PATH", "/usr/bin:/bin"}, {"GITFOLIO_LANG", "ko"}}, 9, 5)
	dec := xml.NewDecoder(strings.NewReader(p))
	for {
		if _, err := dec.Token(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, p)
		}
	}
	for _, want := range []string{"<string>/Users/a&amp;b/bin/gitfolio</string>", "<string>sync</string>", "<key>Hour</key>\n\t\t<integer>9</integer>", "<key>Minute</key>\n\t\t<integer>5</integer>", "/tmp/log &lt;1&gt;", "<key>GITFOLIO_LANG</key>\n\t\t<string>ko</string>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
}

func TestSystemdUnits(t *testing.T) {
	service, timer := systemdUnits("/opt/my apps/gitfolio", [][2]string{{"PATH", "/usr/bin:/100%"}, {"GITFOLIO_LANG", "ko"}}, 9, 5)
	for _, want := range []string{`ExecStart="/opt/my apps/gitfolio" sync`, `Environment="PATH=/usr/bin:/100%%"`, `Environment="GITFOLIO_LANG=ko"`, "Type=oneshot"} {
		if !strings.Contains(service, want) {
			t.Errorf("service lacks %q:\n%s", want, service)
		}
	}
	for _, want := range []string{"OnCalendar=*-*-* 09:05:00", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer lacks %q:\n%s", want, timer)
		}
	}
}

// The user's own crontab lines, blank lines included, are kept; only GitFolio's line changes.
func TestCronWith(t *testing.T) {
	mine := "# my jobs\n0 1 * * * backup\n\n5 9 * * * 'old' sync " + cronMarker + "\n"
	line := "30 8 * * * 'new' sync " + cronMarker
	if got, want := cronWith(mine, line), "# my jobs\n0 1 * * * backup\n\n"+line+"\n"; got != want {
		t.Errorf("replace:\n%q\nwant\n%q", got, want)
	}
	if got, want := cronWith(mine, ""), "# my jobs\n0 1 * * * backup\n\n"; got != want {
		t.Errorf("remove:\n%q\nwant\n%q", got, want)
	}
	if got := cronWith("", line); got != line+"\n" {
		t.Errorf("empty crontab: %q", got)
	}
	if got := cronWith("5 9 * * * 'old' sync "+cronMarker+"\n", ""); got != "" {
		t.Errorf("only ours removed: %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}

func TestTaskXML(t *testing.T) {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local)
	x := taskXML(`C:\my apps\gitfolio.exe`, `C:\data\schedule.log`, [][2]string{{"PATH", `C:\bin`}, {"GITFOLIO_LANG", "ko"}}, 9, 5, day)
	var task struct {
		Start string `xml:"Triggers>CalendarTrigger>StartBoundary"`
		Late  bool   `xml:"Settings>StartWhenAvailable"`
		Cmd   string `xml:"Actions>Exec>Command"`
		Args  string `xml:"Actions>Exec>Arguments"`
	}
	dec := xml.NewDecoder(strings.NewReader(x))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // declared UTF-16, written as a Go string here
	if err := dec.Decode(&task); err != nil {
		t.Fatalf("task XML: %v\n%s", err, x)
	}
	want := `/d /c "set "GITFOLIO_LANG=ko" & "C:\my apps\gitfolio.exe" sync >> "C:\data\schedule.log" 2>&1"`
	if task.Start != "2026-09-30T09:05:00" || !task.Late || task.Cmd != "cmd.exe" || task.Args != want {
		t.Errorf("task = %+v\nwant args %s", task, want)
	}
}

// Without a home or config folder, no unit is read, written or removed in the current folder.
func TestSystemdNeedsConfigDir(t *testing.T) {
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "AppData", "home"} {
		t.Setenv(k, "")
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile(systemdUnit+".timer", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := systemdDir(); err == nil {
		t.Fatal("systemdDir found a folder without a home")
	}
	removeSystemd()
	if _, err := os.Stat(systemdUnit + ".timer"); err != nil {
		t.Error("a unit in the current folder was removed")
	}
}

// Local mode sends nothing by itself, so it takes no daily sync (nothing is registered with the OS).
func TestScheduleRefusedInLocalMode(t *testing.T) {
	t.Setenv("GITFOLIO_LANG", "en")
	dir := t.TempDir()
	if err := saveConfig(dir, Config{AutoSyncOff: true}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSchedule(dir, []string{"09:00"}); err == nil || !strings.Contains(err.Error(), "Local mode") {
		t.Errorf("schedule in local mode: %v", err)
	}
	if cfg, _ := loadConfig(dir); cfg.Schedule != "" {
		t.Errorf("schedule saved: %q", cfg.Schedule)
	}
}

// A build from source registers its own daily sync: the OS schedulers are per user, not per home
// folder, so with the release's names a test run could replace the job of the installed release.
func TestScheduleNamesFromSource(t *testing.T) {
	if !sourceBuild() || launchdLabel != "team.aline.gitfolio.dev" || systemdUnit != "gitfolio-sync-dev" ||
		cronMarker != "# gitfolio-schedule-dev" || winTaskName != `GitFolio\SyncDev` {
		t.Errorf("source build: %s %s %q %s", launchdLabel, systemdUnit, cronMarker, winTaskName)
	}
	if cronWith("5 9 * * * 'brew' sync # gitfolio-schedule\n", "") == "" {
		t.Error("a source build removed the release's cron line")
	}
	defer func(v string) { version = v }(version)
	version = "0.6.0"
	if fromSource(".dev") != "" {
		t.Error("a release takes the source build's names")
	}
}

// The scheduled run is this gitfolio, on this one's data: never another gitfolio found on PATH, and it
// gets the home and config folders this one has (a test HOME too).
func TestScheduleRunsThisCopy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "gitfolio"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	other, same := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(other, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(exe, filepath.Join(same, name)); err != nil {
		t.Skip("no hard link here:", err)
	}
	for dir, want := range map[string]string{other: exe, same: filepath.Join(same, name)} {
		t.Setenv("PATH", dir)
		if got, err := gitfolioPath(); err != nil || got != want {
			t.Errorf("PATH %s: %s %v, want %s", dir, got, err, want)
		}
	}

	t.Setenv("HOME", filepath.Join(other, "home"))
	t.Setenv("XDG_CONFIG_HOME", "")
	env := map[string]string{}
	for _, kv := range scheduleEnv() {
		env[kv[0]] = kv[1]
	}
	if env["HOME"] != filepath.Join(other, "home") || env["PATH"] != os.Getenv("PATH") {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["XDG_CONFIG_HOME"]; ok {
		t.Errorf("an empty XDG_CONFIG_HOME was passed on: %v", env)
	}
}

// A crontab that cannot be read (the user may not use cron) is never rewritten; removing GitFolio's line
// from it is not an error, so schedule on Linux works with the systemd timer.
func TestCronUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in crontab is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte("#!/bin/sh\necho 'You are not allowed to use this program' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := cronSchedule("", "", -1, -1); err != nil {
		t.Errorf("removing: %v", err)
	}
	if err := cronSchedule("/bin/gitfolio", "/tmp/log", 9, 5); err == nil {
		t.Error("adding to an unreadable crontab did not fail")
	}
}
