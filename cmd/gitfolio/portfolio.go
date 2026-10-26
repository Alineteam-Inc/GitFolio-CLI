package main

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const portfolioUsage = "gitfolio portfolio [file.md|file.pdf] [--from file.md]"

// cmdPortfolio writes the user's portfolio from their git history: the developer type and tech stack
// periods aline.team works out, and the repositories as collected on this computer. Markdown, to edit
// into a real portfolio, or a PDF printed with a Chrome-family browser: by the file's name, else asked
// at a terminal (Markdown elsewhere). --from prints a Markdown file instead (one written here and edited, say).
func cmdPortfolio(dir string, args []string) error {
	lang := detectLang(os.Getenv)
	var out, from string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--from" && i+1 < len(args):
			i++
			from = args[i]
		case !strings.HasPrefix(args[i], "-") && out == "":
			out = args[i]
		default:
			return wrongWord("portfolio", args[i], []string{"--from"}, portfolioUsage)
		}
	}
	switch {
	case out != "":
	case from != "":
		out = strings.TrimSuffix(from, filepath.Ext(from)) + ".pdf"
	case interactive() && askPDF(lang):
		out = "portfolio.pdf"
	default:
		out = "portfolio.md"
	}
	pdf := strings.EqualFold(filepath.Ext(out), ".pdf")
	if from != "" && !pdf {
		return failure("usage", portfolioUsage)
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if _, err := os.Stat(out); err == nil { // never over an edited portfolio unasked
		if !interactive() {
			return failure("portfolioExists", tildePath(out))
		}
		if !askYesNo(lang, "portfolioOverwrite", false, tildePath(out)) {
			return nil
		}
	}
	var md, base string
	if from != "" {
		b, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		md = string(b)
		if base, err = filepath.Abs(filepath.Dir(from)); err != nil { // its pictures are found next to it
			return err
		}
	} else if md, err = portfolioOf(dir, lang, false); err != nil {
		return err
	}
	if !pdf {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			return err
		}
		say(lang, "portfolioSaved", tildePath(out))
		return nil
	}
	if err := printPDF(portfolioPage(lang, md, base), out); err != nil {
		return err
	}
	say(lang, "portfolioSavedPDF", tildePath(out))
	return nil
}

// askPDF asks which format to write when no file is named: Markdown (the default) or PDF.
func askPDF(lang string) bool {
	for {
		switch strings.ToLower(prompt(tr(lang, "portfolioAsk"))) {
		case "", "md", "m", "markdown", "1":
			return false
		case "pdf", "p", "2":
			return !stdinClosed
		}
		if stdinClosed {
			return false
		}
		notice(tr(lang, "portfolioFormat"))
	}
}

// portfolioOf is the portfolio in Markdown. In local mode the statistics go first, as for gitfolio
// stats. A part aline.team cannot give now is left out with a warning. For AI tools (ai) the
// repositories are as config ai lets them see them.
func portfolioOf(dir, lang string, ai bool) (string, error) {
	c, err := newClient(dir)
	if err != nil {
		return "", err
	}
	if !c.ensureLogin() {
		return "", failure("loginFirst")
	}
	local, err := sendIfLocal(dir, lang, c)
	if err != nil {
		return "", err
	}
	var repos []Repo
	var commits []Commit
	if err := withLock(dir, func() error { // a push may be writing them
		var err error
		if repos, err = loadRepos(dir); err != nil {
			return err
		}
		commits, err = readCommits(dir)
		return err
	}); err != nil {
		return "", err
	}
	if ai {
		repos = aiView(lang, repos)
	}
	d, _, err := devTypeNow(dir, lang, c, local)
	if err != nil { // what was kept may be another account's, last year's or in another language
		warn(lang, "portfolioNoType", err)
		d = devTypeCache{}
	}
	deps, err := loadDeps(dir)
	if err != nil {
		warn(lang, "portfolioNoStack", err)
	}
	u := localStack(repos, commits, deps) // of the repositories shown (aiView for AI tools)
	return portfolioMarkdown(lang, time.Now(), d, u, repos, commits), nil
}

// portfolioMarkdown lays the portfolio out: who the user is as a developer, the tech stack, then the
// repositories, the latest work first.
func portfolioMarkdown(lang string, now time.Time, d devTypeCache, u techUsages, repos []Repo, commits []Commit) string {
	type entry struct {
		r Repo
		s repoStatus
	}
	var list []entry
	var total int
	var first, last time.Time
	for _, r := range repos {
		s := statusOf(r, commits, nil, syncState{})
		if s.commits == 0 {
			continue
		}
		list = append(list, entry{r, s})
		total += s.commits
		if first.IsZero() || s.first.Before(first) {
			first = s.first
		}
		if s.last.After(last) {
			last = s.last
		}
	}
	slices.SortStableFunc(list, func(a, b entry) int { return b.s.last.Compare(a.s.last) })
	period := "-"
	if total > 0 {
		period = first.Local().Format("2006-01") + " ~ " + last.Local().Format("2006-01")
	}
	var b strings.Builder
	fmt.Fprintf(&b, tr(lang, "portfolioHead"), now.Format(time.DateOnly), len(list), thousands(total), period)

	if t := d.Detail; t.Title != "" {
		about := "\n"
		for _, s := range []string{t.Description, t.HashTags} {
			if s = strings.TrimSpace(s); s != "" {
				about += s + "\n\n"
			}
		}
		fmt.Fprintf(&b, "\n"+tr(lang, "portfolioType"), t.Title, cmp.Or(t.Position, "-"), d.Year, about,
			number(t.Agility), number(t.AgilityAI), number(t.Stability), number(t.StabilityAI), number(t.Contribution), number(t.ContribAI),
			number(t.Adaptability), number(t.AdaptAI), number(t.Consistency), number(t.ConsistAI), cmp.Or(t.TitleAI, "-"))
	}

	var rows strings.Builder
	for _, g := range []struct {
		kind  string
		items []techUsage
	}{{"portfolioLanguage", u.Languages}, {"portfolioFramework", u.Frameworks}, {"portfolioLibrary", u.Libraries}} {
		for _, t := range g.items {
			fmt.Fprintf(&rows, "| %s | %s | %s ~ %s | %s | %s |\n", tr(lang, g.kind), strings.ReplaceAll(t.Name, "|", `\|`),
				yearMonth(t.StartDate), yearMonth(t.EndDate), number(t.CareerMonths), number(t.ProjectCount))
		}
	}
	if rows.Len() > 0 {
		b.WriteString("\n" + tr(lang, "portfolioStack") + rows.String())
	}

	if len(list) > 0 {
		b.WriteString("\n" + tr(lang, "portfolioRepos"))
	}
	for _, e := range list {
		var where []string
		if e.r.Namespace != "" {
			where = append(where, strings.TrimSpace(providerNames[e.r.Provider]+" "+e.r.Namespace))
		}
		where = append(where, e.s.first.Local().Format(time.DateOnly)+" ~ "+e.s.last.Local().Format(time.DateOnly))
		fmt.Fprintf(&b, "\n"+tr(lang, "portfolioRepo"), e.r.Name, strings.Join(where, " · "), thousands(e.s.commits),
			e.s.aiShare(), thousands(e.s.add), thousands(e.s.del), thousands(e.s.created), e.s.languageShares(5))
	}
	return b.String()
}

// providerNames are the git services as people write them; any other host is shown by its path alone.
var providerNames = map[string]string{"GITHUB": "GitHub", "GITLAB": "GitLab", "BITBUCKET": "Bitbucket", "DEVOPS": "Azure DevOps"}

// yearMonth is "2026-10" of a date such as "2026-10-05T09:00:00", "-" for none.
func yearMonth(s string) string { return cmp.Or(s[:min(len(s), 7)], "-") }

// number is a count or score aline.team may leave out: "-" then.
func number(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(*v)
}

// thousands writes n with a comma between each three digits: 1,780.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// portfolioPage is the Markdown as a page to print: GitFolio's own simple reading of it (headings,
// paragraphs, lists, tables, quotes, code, links and pictures), enough for what it writes and for the
// same written by hand. Anything else shows as text. base is the folder relative pictures are in.
func portfolioPage(lang, md, base string) string {
	var body strings.Builder
	title := "Portfolio"
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	starts := func(t string) bool {
		return t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") ||
			strings.HasPrefix(t, "```") || mdItem.MatchString(t) || mdRule.MatchString(t)
	}
	for i := 0; i < len(lines); {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "":
			i++
		case strings.HasPrefix(t, "```"):
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				j++
			}
			body.WriteString("<pre><code>" + html.EscapeString(strings.Join(lines[i+1:min(j, len(lines))], "\n")) + "</code></pre>\n")
			i = j + 1
		case mdHeading.MatchString(t):
			m := mdHeading.FindStringSubmatch(t)
			if len(m[1]) == 1 && title == "Portfolio" {
				title = m[2]
			}
			fmt.Fprintf(&body, "<h%d>%s</h%[1]d>\n", len(m[1]), inline(m[2]))
			i++
		case mdRule.MatchString(t):
			body.WriteString("<hr>\n")
			i++
		case strings.HasPrefix(t, "|"):
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				rows = append(rows, mdRow(strings.TrimSpace(lines[i])))
			}
			body.WriteString(mdTable(rows))
		case mdItem.MatchString(t):
			tag := "ul"
			if m := mdItem.FindStringSubmatch(t); m[1] != "-" && m[1] != "*" && m[1] != "+" {
				tag = "ol"
			}
			body.WriteString("<" + tag + ">\n")
			for ; i < len(lines) && mdItem.MatchString(strings.TrimSpace(lines[i])); i++ {
				body.WriteString("<li>" + inline(mdItem.FindStringSubmatch(strings.TrimSpace(lines[i]))[2]) + "</li>\n")
			}
			body.WriteString("</" + tag + ">\n")
		case strings.HasPrefix(t, ">"):
			var quote []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			body.WriteString("<blockquote><p>" + inline(strings.Join(quote, " ")) + "</p></blockquote>\n")
		default:
			var para []string
			for ; i < len(lines) && (len(para) == 0 || !starts(strings.TrimSpace(lines[i]))); i++ {
				para = append(para, strings.TrimSpace(lines[i]))
			}
			body.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
		}
	}
	baseTag := ""
	if base != "" {
		baseTag = `<base href="` + html.EscapeString(fileURL(base)+"/") + `">`
	}
	return `<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><title>` + html.EscapeString(title) +
		"</title>" + baseTag + "<style>" + portfolioCSS + "</style></head><body>\n" + body.String() + "</body></html>\n"
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	mdRule    = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	mdItem    = regexp.MustCompile(`^([-*+]|\d+[.)])\s+(.*)$`)
	mdAlign   = regexp.MustCompile(`^:?-+:?$`)
	mdImage   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdBold    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdItalic  = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
)

// mdRow splits a table row into its cells; "\|" stays in its cell.
func mdRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(strings.ReplaceAll(line, `\|`, "\x00"), "|"), "|")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.ReplaceAll(strings.TrimSpace(c), "\x00", "|")
	}
	return cells
}

// mdTable is a table whose second row is the "| --- | ---: |" line: a header above it, and the cells
// of a column ending in ":" aligned right. Without that line every row is a body row.
func mdTable(rows [][]string) string {
	var b strings.Builder
	b.WriteString("<table>\n")
	right := map[int]bool{}
	if len(rows) > 1 && !slices.ContainsFunc(rows[1], func(c string) bool { return !mdAlign.MatchString(c) }) {
		for i, c := range rows[1] {
			right[i] = strings.HasSuffix(c, ":")
		}
		b.WriteString("<thead>" + mdTableRow(rows[0], "th", right) + "</thead>\n")
		rows = rows[2:]
	}
	b.WriteString("<tbody>\n")
	for _, r := range rows {
		b.WriteString(mdTableRow(r, "td", right) + "\n")
	}
	b.WriteString("</tbody></table>\n")
	return b.String()
}

func mdTableRow(cells []string, tag string, right map[int]bool) string {
	var b strings.Builder
	b.WriteString("<tr>")
	for i, c := range cells {
		attr := ""
		if right[i] {
			attr = ` class="num"`
		}
		b.WriteString("<" + tag + attr + ">" + inline(c) + "</" + tag + ">")
	}
	return b.String() + "</tr>"
}

// inline turns code spans, pictures, links, bold and italic into HTML, everything else escaped.
// A link to a script is left as text.
func inline(s string) string {
	var b strings.Builder
	parts := strings.Split(s, "`")
	for i, p := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString("<code>" + html.EscapeString(p) + "</code>")
			continue
		}
		if i%2 == 1 { // a backtick never closed
			b.WriteString("`")
		}
		p = html.EscapeString(p)
		p = mdImage.ReplaceAllStringFunc(p, func(m string) string {
			g := mdImage.FindStringSubmatch(m)
			if unsafeLink(g[2]) {
				return m
			}
			return `<img alt="` + g[1] + `" src="` + g[2] + `">`
		})
		p = mdLink.ReplaceAllStringFunc(p, func(m string) string {
			g := mdLink.FindStringSubmatch(m)
			if unsafeLink(g[2]) {
				return m
			}
			return `<a href="` + g[2] + `">` + g[1] + `</a>`
		})
		p = mdBold.ReplaceAllString(p, "<strong>$1</strong>")
		p = mdItalic.ReplaceAllString(p, "<em>$1</em>")
		b.WriteString(p)
	}
	return b.String()
}

func unsafeLink(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(u, "javascript:") || strings.HasPrefix(u, "data:text")
}

// fileURL is the file: URL of an absolute path, on Windows too (file:///C:/Users/...).
func fileURL(p string) string {
	return (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(p), "/")}).String()
}

const portfolioCSS = `@page { size: A4; margin: 16mm 15mm; }
body { font-family: -apple-system, "Segoe UI", "Apple SD Gothic Neo", "Malgun Gothic", "Noto Sans CJK KR", "Noto Sans KR",
  "Hiragino Sans", "Yu Gothic", "Noto Sans CJK JP", sans-serif; font-size: 10.5pt; line-height: 1.55; color: #1f2328; }
h1 { font-size: 22pt; margin: 0 0 4pt; }
h2 { font-size: 14pt; margin: 18pt 0 6pt; padding-bottom: 3pt; border-bottom: 1px solid #d0d7de; }
h3 { font-size: 11.5pt; margin: 12pt 0 2pt; }
h2, h3 { break-after: avoid; }
p, ul, ol, table, blockquote, pre { margin: 0 0 7pt; }
ul, ol { padding-left: 16pt; }
table { border-collapse: collapse; font-size: 9.5pt; }
th, td { border: 1px solid #d0d7de; padding: 3pt 6pt; text-align: left; vertical-align: top; }
th { background: #f6f8fa; }
td.num, th.num { text-align: right; }
tr { break-inside: avoid; }
code, pre { font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 9pt; background: #f6f8fa; }
pre { padding: 6pt; white-space: pre-wrap; }
blockquote { margin-left: 0; padding-left: 9pt; border-left: 3px solid #d0d7de; color: #59636e; }
img { max-width: 100%; }
a { color: #0969da; text-decoration: none; }
hr { border: 0; border-top: 1px solid #d0d7de; }
`

// printPDF prints the page to out with a Chrome-family browser, without a window. The page and the
// browser's throwaway profile sit in a folder next to out, where a sandboxed browser (snap) can also
// read and write; it is removed afterwards.
func printPDF(page, out string) error {
	browser := findBrowser()
	if browser == "" {
		return failure("portfolioNoBrowser")
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), "gitfolio-portfolio-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	src, pdf := filepath.Join(tmp, "portfolio.html"), filepath.Join(tmp, "portfolio.pdf")
	if err := os.WriteFile(src, []byte(page), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir=" + filepath.Join(tmp, "profile"), "--no-pdf-header-footer", "--print-to-pdf-no-header",
		"--print-to-pdf=" + pdf, fileURL(src)}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 { // as root (a container) Chrome runs only without its sandbox
		args = append([]string{"--no-sandbox"}, args...)
	}
	cmd := exec.CommandContext(ctx, browser, args...)
	output, runErr := cmd.CombinedOutput()
	if fi, err := os.Stat(pdf); err != nil || fi.Size() == 0 {
		return failure("portfolioPDFFailed", filepath.Base(browser), cmp.Or(lastLine(string(output)), fmt.Sprint(runErr)))
	}
	return os.Rename(pdf, out)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// findBrowser finds a Chrome-family browser: CHROME_PATH, then where each system installs one.
func findBrowser() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Chromium", "Brave Browser"} {
			for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
				paths = append(paths, filepath.Join(root, app+".app", "Contents", "MacOS", app))
			}
		}
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				paths = append(paths, filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
