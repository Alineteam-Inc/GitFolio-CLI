package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPortfolioMarkdown(t *testing.T) {
	n := func(v int) *int { return &v }
	d := devTypeCache{Year: 2026, Detail: devType{Title: "SPRINTER", Description: "Ships fast.", HashTags: "#fast #steady",
		Position: "BACKEND", Agility: n(72), AgilityAI: n(75), Stability: n(60)}}
	u := techUsages{
		Languages: []techUsage{{Name: "Go", StartDate: "2025-03-02T10:00:00", EndDate: "2026-10-05T09:00:00", CareerMonths: n(20), ProjectCount: n(3)}},
		Libraries: []techUsage{{Name: "a|b", StartDate: "2026-01-01"}},
	}
	repos := []Repo{{ID: "t", Name: "tool"}, {ID: "a", Name: "app", Provider: "GITHUB", Namespace: "me/app"}, {ID: "e", Name: "empty"}}
	commits := []Commit{
		{Repo: "t", Hash: "3", Date: "2025-05-01T12:00:00Z", CreationType: creationHuman, Files: []FileStat{{Name: "x.py", Add: 3, Del: 1}}},
		{Repo: "a", Hash: "1", Date: "2026-09-01T12:00:00Z", CreationType: creationHuman, Files: []FileStat{{Name: "main.go", Add: 10, Del: 2, Created: true}}},
		{Repo: "a", Hash: "2", Date: "2026-09-02T12:00:00Z", CreationType: creationHumanCoAI, Files: []FileStat{{Name: "main.go", Add: 1200}, {Name: "README.md", Add: 5}}},
	}
	got := portfolioMarkdown("en", time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), d, u, repos, commits)
	want := `# Development portfolio

Made by GitFolio from my git history on 2026-10-21. Repositories: 2, commits: 3, period: 2025-05 ~ 2026-09.

## Developer type

**SPRINTER** · main position BACKEND · 2026, by aline.team

Ships fast.

#fast #steady

| Score | Human only | With AI |
| --- | ---: | ---: |
| Agility | 72 | 75 |
| Stability | 60 | - |
| Contribution | - | - |
| Adaptability | - | - |
| Consistency | - | - |

Type with AI: -

## Tech stack

From the first to the last commit of the repositories using each, as aline.team works it out.

| Kind | Name | Period | Months | Repositories |
| --- | --- | --- | ---: | ---: |
| Language | Go | 2025-03 ~ 2026-10 | 20 | 3 |
| Library | a\|b | 2026-01 ~ - | - | - |

## Repositories

### app

GitHub me/app · 2026-09-01 ~ 2026-09-02

- Commits: 2 (with AI: 50%)
- Lines: +1,215 / -2, new files: 1
- Languages by changed lines: Go 100%

### tool

2025-05-01 ~ 2025-05-01

- Commits: 1 (with AI: 0%)
- Lines: +3 / -1, new files: 0
- Languages by changed lines: Python 100%
`
	if got != want {
		t.Errorf("portfolio =\n%s\nwant\n%s", got, want)
	}
	if got := portfolioMarkdown("en", time.Now(), devTypeCache{}, techUsages{}, nil, nil); strings.Contains(got, "##") {
		t.Errorf("an empty portfolio has sections:\n%s", got)
	}
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4321: "-4,321"} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// The page GitFolio prints reads its own Markdown and the same written by hand, and lets nothing run.
func TestPortfolioPage(t *testing.T) {
	md := "# Me & <you>\n\nIntro with **bold**, *it*, `a <b>`, [site](https://example.com) and [x](javascript:alert(1)).\n" +
		"Still the same paragraph.\n\n| Kind | Count |\n| --- | ---: |\n| a\\|b | 3 |\n\n- one\n- two\n\n1. first\n\n> quoted\n\n```\n<script>x</script>\n```\n\n---\n\n![me](me.png)\n<script>alert(1)</script>\n"
	got := portfolioPage("ko", md, "/home/me/cv")
	for _, want := range []string{
		`<html lang="ko">`, "<title>Me &amp; &lt;you&gt;</title>", `<base href="file:///home/me/cv/">`,
		"<h1>Me &amp; &lt;you&gt;</h1>",
		"<p>Intro with <strong>bold</strong>, <em>it</em>, <code>a &lt;b&gt;</code>, <a href=\"https://example.com\">site</a> and [x](javascript:alert(1)). Still the same paragraph.</p>",
		"<thead><tr><th>Kind</th><th class=\"num\">Count</th></tr></thead>", "<tr><td>a|b</td><td class=\"num\">3</td></tr>",
		"<ul>\n<li>one</li>\n<li>two</li>\n</ul>", "<ol>\n<li>first</li>\n</ol>", "<blockquote><p>quoted</p></blockquote>",
		"<pre><code>&lt;script&gt;x&lt;/script&gt;</code></pre>", "<hr>", `<img alt="me" src="me.png">`, "&lt;script&gt;alert(1)&lt;/script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page has no %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("page runs a script:\n%s", got)
	}
}

func TestPortfolio(t *testing.T) {
	f := &fakeAline{token: testToken, devType: map[string]any{"devTypeTitle": "KEEPER", "agilityStat": 50}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(i func() bool) { interactive = i }(interactive)
	interactive = func() bool { return false }
	var buf bytes.Buffer
	defer func(o, e io.Writer) { out, errOut = o, e }(out, errOut)
	out, errOut = &buf, &buf
	dir, work := t.TempDir(), t.TempDir()
	t.Chdir(work)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}))
	must(saveConfig(dir, Config{})) // automatic sync: no sync runs first
	must(saveRepos(dir, []Repo{{ID: "a", Name: "app", Provider: "GITHUB", Namespace: "me/app"}}))
	must(writeCommits(dir, []Commit{{Repo: "a", Hash: "1", Date: "2026-09-01T12:00:00Z", Message: "feat: start", CreationType: creationHuman,
		Files: []FileStat{{Name: "main.go", Add: 1}}}}))

	must(cmdPortfolio(dir, nil))
	md, err := os.ReadFile(filepath.Join(work, "portfolio.md"))
	must(err)
	for _, want := range []string{"**KEEPER**", "| Agility | 50 | - |", "| Language | Go | 2026-09 ~ 2026-09 | 0 | 1 |", "### app"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("portfolio.md has no %q:\n%s", want, md)
		}
	}
	if err := cmdPortfolio(dir, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("an existing portfolio was written over without asking: %v", err)
	}
	for _, args := range [][]string{{"--from", "a.md", "b.md"}, {"--all"}, {"a.md", "b.md"}} {
		if err := cmdPortfolio(dir, args); err == nil || !strings.Contains(err.Error(), "Usage") {
			t.Errorf("portfolio %v: %v", args, err)
		}
	}

	t.Setenv("CHROME_PATH", filepath.Join(work, "no-such-browser"))
	if err := cmdPortfolio(dir, []string{"cv.pdf"}); err == nil {
		t.Error("a PDF without a browser")
	}
	if runtime.GOOS == "windows" {
		return // the stand-in browser below is a shell script
	}
	browser := filepath.Join(t.TempDir(), "chrome")
	must(os.WriteFile(browser, []byte("#!/bin/sh\nfor a; do case \"$a\" in --print-to-pdf=*) printf '%%PDF-1.4' > \"${a#--print-to-pdf=}\";; file:*) page=\"$a\";; esac; done\n"+
		"cat \"${page#file://}\" > \""+filepath.Join(work, "page.html")+"\"\n"), 0o755))
	t.Setenv("CHROME_PATH", browser)
	must(os.WriteFile(filepath.Join(work, "edited.md"), []byte("# My CV\n\nHand written.\n"), 0o644))
	must(cmdPortfolio(dir, []string{"--from", "edited.md"}))
	if pdf, err := os.ReadFile(filepath.Join(work, "edited.pdf")); err != nil || !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Errorf("edited.pdf = %q, %v", pdf, err)
	}
	if page, _ := os.ReadFile(filepath.Join(work, "page.html")); !strings.Contains(string(page), "<h1>My CV</h1>") {
		t.Errorf("the browser printed:\n%s", page)
	}
	if left, _ := filepath.Glob(filepath.Join(work, "gitfolio-portfolio-*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}

	// At a terminal with no file named, the format is asked: a wrong answer is asked again.
	defer func(r *bufio.Reader) { stdin = r }(stdin)
	interactive, stdin = func() bool { return true }, bufio.NewReader(strings.NewReader("docx\npdf\n"))
	must(cmdPortfolio(dir, nil))
	if _, err := os.Stat(filepath.Join(work, "portfolio.pdf")); err != nil || !strings.Contains(buf.String(), "Answer md or pdf") {
		t.Errorf("asked for pdf: %v\n%s", err, buf.String())
	}
}

// When aline.team cannot give the developer type, the portfolio goes without one: the type kept here may
// be another account's, last year's or in another language.
func TestPortfolioWithoutAnotherAccountsType(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer ts.Close()
	t.Setenv("GITFOLIO_API_URL", ts.URL)
	t.Setenv("GITFOLIO_LANG", "en")
	defer func(o, e io.Writer) { out, errOut = o, e }(out, errOut)
	out, errOut = io.Discard, io.Discard
	dir := t.TempDir()
	if err := saveCredentials(dir, Credentials{Token: testToken, Email: "dev@example.com", Server: ts.URL}); err != nil {
		t.Fatal(err)
	}
	other := devTypeCache{Year: time.Now().Year(), Account: "someone@else.example", Lang: "en", Detail: devType{Title: "ARCHITECT"}}
	if err := saveJSON(filepath.Join(dir, devTypeFile), other); err != nil {
		t.Fatal(err)
	}
	if md, err := portfolioOf(dir, "en", false); err != nil || strings.Contains(md, "ARCHITECT") {
		t.Errorf("portfolio with aline.team down: %v\n%s", err, md)
	}
}
