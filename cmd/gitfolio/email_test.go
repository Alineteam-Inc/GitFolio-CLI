package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEmailCommands(t *testing.T) {
	dir := t.TempDir()
	emails := func() []string {
		t.Helper()
		cfg, err := loadConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Emails
	}
	for _, args := range [][]string{{"add", "Me@Home.com", "me@work.com"}, {"add", "me@work.com"}, {"primary", "me@work.com"}, {"rm", "me@home.com"}} {
		if err := cmdEmail(dir, args); err != nil {
			t.Fatalf("email %v: %v", args, err)
		}
	}
	if got := emails(); !slices.Equal(got, []string{"me@work.com"}) {
		t.Errorf("emails = %v, want [me@work.com] (lowercased, no duplicates, primary first)", got)
	}
	for _, bad := range [][]string{{"rm", "me@work.com"}, {"rm", "nobody@x.com"}, {"add", "not-an-email"}, {"primary", "a b@x.com"}, {"frob", "x@y.z"}} {
		if cmdEmail(dir, bad) == nil {
			t.Errorf("email %v succeeded", bad)
		}
	}
	if got := emails(); !slices.Equal(got, []string{"me@work.com"}) {
		t.Errorf("a refused command changed the emails: %v", got)
	}
}

// Commits by any work email or by the repository's git email are the user's; the first repository
// registered brings its git email in as the primary one.
func TestWorkEmails(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, dir := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "Repo@Example.com"}, {"config", "user.name", "me"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mine, err := myEmails(repo, []string{"Work@Example.com"})
	if err != nil || !mine["work@example.com"] || !mine["repo@example.com"] || len(mine) != 2 {
		t.Errorf("myEmails = %v, %v", mine, err)
	}
	if _, err := registerRepo(dir, newRepo(repo), false); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := loadConfig(dir); !slices.Equal(cfg.Emails, []string{"repo@example.com"}) || primaryEmail(cfg) != "repo@example.com" {
		t.Errorf("after the first add: emails %v, primary %q", cfg.Emails, primaryEmail(cfg))
	}
}

// A work email is verified with a code (a wrong code is asked again), an already verified one needs no
// code, the ✔ marks follow what aline.team says, and removing a verified email removes it there too.
func TestVerifyWorkEmail(t *testing.T) {
	f := &fakeAline{token: testToken}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	t.Setenv("GITFOLIO_API_URL", srv.URL)
	dir := t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{"000000", "123456"}
	ask := func(retry bool, length int) string {
		code := codes[0]
		codes = codes[1:]
		return code
	}
	if err := c.verifyEmail("Me@Work.com", ask); err != nil || len(codes) != 0 {
		t.Fatalf("verify = %v with codes left %v", err, codes)
	}
	if saved, _ := loadCredentials(dir); !verified(saved, "me@work.com") || !verified(saved, "DEV@example.com") || verified(saved, "other@x.com") {
		t.Errorf("verified emails kept = %v", saved.Verified)
	}
	if err := c.verifyEmail("later@work.com", func(bool, int) string { return "" }); !errors.Is(err, errSkipped) || verified(c.creds, "later@work.com") {
		t.Errorf("an empty code = %v, want it skipped and the email not verified", err)
	}
	if err := c.verifyEmail("me@work.com", func(bool, int) string { t.Error("asked a code for a verified email"); return "" }); err != nil {
		t.Errorf("verifying again: %v", err)
	}
	cfg := Config{Emails: []string{"me@work.com", "me@home.com"}}
	if got := emailList("en", cfg, c.creds); !strings.Contains(got, "★ me@work.com ✔\n") || !strings.Contains(got, "  me@home.com\n") || !strings.Contains(got, "verify") {
		t.Errorf("list:\n%s", got)
	}
	if got := emailList("en", cfg, Credentials{}); strings.Contains(got, "✔") || strings.Contains(got, "verify") {
		t.Errorf("logged out, the list shows verification:\n%s", got)
	}
	full := f.work
	for len(f.work) < 20 {
		f.work = append(f.work, fmt.Sprintf("w%d@x.com", len(f.work)))
	}
	if err := c.verifyEmail("one@more.com", func(bool, int) string { return "123456" }); err == nil || !strings.Contains(err.Error(), "20") {
		t.Errorf("verify past 20 emails = %v, want the remove-one-first message", err)
	}
	f.work = full
	if err := saveConfig(dir, Config{Emails: []string{"me@home.com", "me@work.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := cmdEmail(dir, []string{"rm", "me@work.com"}); err != nil {
		t.Fatal(err)
	}
	if len(f.work) != 0 {
		t.Errorf("aline.team still has %v verified", f.work)
	}
}

func TestNoreply(t *testing.T) {
	for e, want := range map[string]bool{
		"123+me@users.noreply.github.com":    true,
		"me@users.noreply.github.com":        true, // older form: still no mailbox behind it
		"12-me@users.noreply.gitlab.com":     true,
		"7-me@Users.Noreply.git.example.com": true, // a GitLab server of its own
		"me@noreply.github.com":              false,
		"noreply@example.com":                false,
		"me@work.com":                        false,
	} {
		if noreply(e) != want {
			t.Errorf("noreply(%q) = %v", e, !want)
		}
	}
	got := emailList("en", Config{Emails: []string{"1+me@users.noreply.github.com"}}, Credentials{Token: testToken, Email: "a@b.c"})
	if !strings.Contains(got, "(noreply)") || strings.Contains(got, "verify") {
		t.Errorf("a noreply address is shown as one to verify:\n%s", got)
	}
	// aline.team takes a noreply address once the account its number names is linked there.
	c := &client{base: "https://aline.team/api", creds: Credentials{Email: "a@b.c", Verified: []string{"9+me@users.noreply.github.com"}}}
	for e, want := range map[string]string{
		"a@b.c":                              "",
		"9+me@users.noreply.github.com":      "", // stored by aline.team: linked already
		"me@work.com":                        "code",
		"123+me@users.noreply.github.com":    "github",
		"12-me@users.noreply.gitlab.com":     "gitlab",
		"me@users.noreply.github.com":        "none", // older form: no number
		"x+me@users.noreply.github.com":      "none",
		"7-me@users.noreply.git.example.com": "none", // a GitLab server of its own
	} {
		m := c.verifyJSON(map[string]any{}, e)
		if by, _ := m["verifyBy"].(string); by != want {
			t.Errorf("verifyBy(%q) = %v, want %q", e, m["verifyBy"], want)
		}
		if url, _ := m["verifyUrl"].(string); (url != "") != (want == "github" || want == "gitlab") || (url != "" && url != "https://aline.team/settings/manage-git") {
			t.Errorf("verifyUrl(%q) = %q", e, url)
		}
	}
}

// The emails git uses in the found repositories are offered: the global one first, then each
// repository's own, with how many repositories use each. Without a terminal init keeps what it has.
func TestRepoEmails(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\temail = Me@Home.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	var repos []string
	for _, local := range []string{"", "me@work.com", "me@work.com"} {
		r := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", r).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		if local != "" {
			if out, err := exec.Command("git", "-C", r, "config", "user.email", local).CombinedOutput(); err != nil {
				t.Fatalf("git config: %v %s", err, out)
			}
		}
		repos = append(repos, r)
	}
	emails, uses := repoEmails(repos)
	if !slices.Equal(emails, []string{"me@home.com", "me@work.com"}) || uses["me@home.com"] != 1 || uses["me@work.com"] != 2 {
		t.Errorf("emails = %v, uses = %v", emails, uses)
	}
	if got := chooseEmails("en", Credentials{}, nil, repos); !slices.Equal(got, []string{"me@home.com"}) {
		t.Errorf("without a terminal and no emails yet: %v, want the global git email", got)
	}
	if got := chooseEmails("en", Credentials{}, []string{"old@job.com"}, repos); !slices.Equal(got, []string{"old@job.com"}) {
		t.Errorf("without a terminal: %v, want the emails so far", got)
	}
}
