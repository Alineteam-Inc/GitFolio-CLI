package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The runner's display language is English; a Korean or Japanese Windows gives ko or ja.
func TestDeviceLanguagesWindows(t *testing.T) {
	if got := deviceLanguages(); len(got) != 1 || (got[0] != "en" && got[0] != "ko" && got[0] != "ja") {
		t.Errorf("deviceLanguages() = %v", got)
	}
}

// TestTaskSchedulerWindows registers the daily sync with the real Task Scheduler under a test name,
// reads it back and removes it.
func TestTaskSchedulerWindows(t *testing.T) {
	old := winTaskName
	winTaskName = fmt.Sprintf(`GitFolioTest\Sync%d`, os.Getpid())
	defer func() { winTaskName = old }()
	dir := t.TempDir()
	logTo, err := schedule(dir, `C:\Program Files\GitFolio\gitfolio.exe`, 9, 5)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("schtasks", "/Query", "/TN", winTaskName, "/XML").Output()
	if err != nil {
		t.Fatalf("task not registered: %v", err)
	}
	x := string(out)
	for _, want := range []string{"T09:05:00", "<StartWhenAvailable>true</StartWhenAvailable>", "cmd.exe", `C:\Program Files\GitFolio\gitfolio.exe`, logTo} {
		if !strings.Contains(x, want) && !strings.Contains(x, strings.ReplaceAll(want, `"`, "&quot;")) {
			t.Errorf("registered task lacks %q:\n%s", want, x)
		}
	}
	if err := unschedule(); err != nil {
		t.Fatal(err)
	}
	if exec.Command("schtasks", "/Query", "/TN", winTaskName).Run() == nil {
		t.Error("task still registered after unschedule")
	}
	if err := unschedule(); err != nil { // nothing registered: not an error
		t.Error(err)
	}
}
