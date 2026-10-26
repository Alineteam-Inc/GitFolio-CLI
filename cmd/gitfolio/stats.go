package main

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// devTypeFile keeps the last developer type aline.team made for this account, shown by gitfolio stats.
const devTypeFile = "devtype.json"

type devTypeCache struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Year      int       `json:"year"`
	Account   string    `json:"account"`        // the account it belongs to: another login fetches again
	Lang      string    `json:"lang,omitempty"` // the language its texts are in: another one fetches again
	Detail    devType   `json:"detail"`
}

// devType is what gitfolio stats shows of aline.team's developer type: the type, its five scores
// (0-100) and the same for AI-assisted work. Only this is kept; the rest of aline.team's answer (the
// account email, the share code, the reasons) is not.
type devType struct {
	Title        string `json:"devTypeTitle"`
	Description  string `json:"devTypeDescription"`
	HashTags     string `json:"devTypeHashTags"`
	ComputedAt   string `json:"devTypeComputedAt"`
	Position     string `json:"dominantPosition"`
	Agility      *int   `json:"agilityStat"`
	Stability    *int   `json:"stabilityStat"`
	Contribution *int   `json:"contributionStat"`
	Adaptability *int   `json:"adaptabilityStat"`
	Consistency  *int   `json:"consistencyStat"`
	TitleAI      string `json:"devTypeTitleAi"`
	AgilityAI    *int   `json:"agilityStatAi"`
	StabilityAI  *int   `json:"stabilityStatAi"`
	ContribAI    *int   `json:"contributionStatAi"`
	AdaptAI      *int   `json:"adaptabilityStatAi"`
	ConsistAI    *int   `json:"consistencyStatAi"`
}

// cmdStats shows the developer type aline.team made from the commits sent: the one saved here, or, when
// there is none for this year and account (or with --refresh), the one aline.team has or makes now,
// saved for next time.
func cmdStats(dir string, args []string) error {
	lang := detectLang(os.Getenv)
	jsonMode(args)
	refresh := false
	for _, a := range args {
		switch a {
		case "--refresh":
			refresh = true
		case "--json":
		default:
			return wrongWord("stats", a, []string{"--refresh", "--json"}, "gitfolio stats [--refresh] [--json]")
		}
	}
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	if local, err := sendIfLocal(dir, lang, c); err != nil {
		return err
	} else if local {
		refresh = true
	}
	cache, saved, err := devTypeNow(dir, lang, c, refresh)
	if err != nil {
		return err
	}
	if jsonOut {
		var d *devType // null: nothing to make a type from yet
		if cache.Detail.Title != "" {
			d = &cache.Detail
		}
		return printJSON(map[string]any{"year": cache.Year, "saved": saved, "fetchedAt": cache.FetchedAt, "type": d})
	}
	showDevType(lang, cache, saved)
	return nil
}

// devTypeNow is the developer type: the one saved here, or, when there is none for this year and
// account (or with refresh), the one aline.team has or makes now, saved for next time. saved tells
// it is the saved one, also when aline.team could not be asked.
func devTypeNow(dir, lang string, c *client, refresh bool) (cache devTypeCache, saved bool, err error) {
	p := filepath.Join(dir, devTypeFile)
	if err := loadJSON(p, &cache); err != nil {
		return cache, false, err
	}
	year := time.Now().Year()
	saved = cache.Detail.Title != "" && cache.Year == year && cache.Account == c.creds.Email && cache.Lang == lang
	if saved && !refresh {
		return cache, true, nil
	}
	if !c.ensureLogin() {
		return cache, false, failure("loginFirst")
	}
	var d devType
	if err := c.call("GET", fmt.Sprintf("/cli/devtype?yearPeriod=%d", year), nil, &d); err != nil {
		if saved { // offline or refused: what was saved still stands
			warn(lang, "statsFetchFailed", err)
			return cache, true, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() { // aline.team finishes it anyway and keeps it
			return cache, false, failure("statsSlow")
		}
		return cache, false, err
	}
	cache = devTypeCache{FetchedAt: time.Now(), Year: year, Account: c.creds.Email, Lang: lang, Detail: d}
	if d.Title != "" { // nothing to analyse yet is not kept
		if err := saveJSON(p, cache); err != nil {
			return cache, false, err
		}
	}
	return cache, false, nil
}

// sendIfLocal sends the statistics before aline.team is asked about them in local mode, which sends
// nothing by itself, and reports whether it is local mode.
func sendIfLocal(dir, lang string, c *client) (bool, error) {
	cfg, err := loadConfig(dir)
	if err != nil || !cfg.AutoSyncOff {
		return false, err
	}
	if !c.ensureLogin() {
		return true, failure("loginFirst")
	}
	err = withLock(dir, func() error {
		n, _, err := syncNow(dir, lang, false)
		if err == nil && !jsonOut { // with --json stdout carries the command's own result alone
			err = reportSync(dir, lang, n, nil)
		}
		return err
	})
	if err != nil {
		warn(lang, "failed", err) // what aline.team has from before still shows
	}
	return true, nil
}

// checkLocalMode tells a user who turned autosync off before 0.6.0, once, that it is now local mode.
// Hooks and pipes are not told.
func checkLocalMode(dir, lang string) {
	cfg, err := loadConfig(dir)
	if err != nil || !cfg.AutoSyncOff || cfg.LocalModeKnown || !interactive() || !terminal(os.Stdout) {
		return
	}
	cfg.LocalModeKnown = true
	if saveConfig(dir, cfg) == nil {
		notice(tr(lang, "localModeOnce"))
	}
}

// techUsage is how long the user used one language, framework or library, as aline.team works it out
// from the repositories: from the first to the last commit of the repositories using it.
type techUsage struct {
	Name           string   `json:"name"`
	StartDate      string   `json:"startDate"`
	EndDate        string   `json:"endDate"`
	CareerMonths   *int     `json:"careerMonths"`
	ProjectCount   *int     `json:"projectCount"`
	ActivityMonths []string `json:"activityMonths"` // the months it was used in ("2026-10"), ascending
}

type techUsages struct {
	Languages  []techUsage `json:"languages"`
	Frameworks []techUsage `json:"frameworks"`
	Libraries  []techUsage `json:"libraries"`
}

// techUsagesNow is the tech stack worked out on this computer from the registered repositories, as
// aline.team works it out (localStack): no login, nothing sent. For AI tools (ai) the repositories
// hidden from them count for nothing (config ai).
func techUsagesNow(dir, lang string, ai bool) (u techUsages, err error) {
	err = withLock(dir, func() error { // a push may be writing them
		repos, err := loadRepos(dir)
		if err != nil {
			return err
		}
		if ai {
			repos = aiView(lang, repos)
		}
		commits, err := readCommits(dir)
		if err != nil {
			return err
		}
		deps, err := loadDeps(dir)
		if err != nil {
			return err
		}
		u = localStack(repos, commits, deps)
		return nil
	})
	return u, err
}

// cmdStack shows the tech stack worked out on this computer (techUsagesNow).
func cmdStack(dir string, args []string) error {
	lang := detectLang(os.Getenv)
	jsonMode(args)
	if len(args) > 0 && !slices.Equal(args, []string{"--json"}) {
		return failure("usage", "gitfolio stack [--json]")
	}
	u, err := techUsagesNow(dir, lang, false)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(u)
	}
	if len(u.Languages)+len(u.Frameworks)+len(u.Libraries) == 0 {
		say(lang, "stackEmpty")
		return nil
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAME\tFROM\tTO\tMONTHS\tPROJECTS")
	for _, g := range []struct {
		kind  string
		items []techUsage
	}{{"language", u.Languages}, {"framework", u.Frameworks}, {"library", u.Libraries}} {
		for _, t := range g.items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", g.kind, t.Name, yearMonth(t.StartDate), yearMonth(t.EndDate), number(t.CareerMonths), number(t.ProjectCount))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	header, rows, _ := strings.Cut(b.String(), "\n")
	show(out, strings.TrimRight(tr(lang, "stackTitle"), "\n")+"\n"+dim(strings.TrimRight(header, " "))+"\n"+rows)
	return nil
}

func showDevType(lang string, cache devTypeCache, saved bool) {
	d := cache.Detail
	if d.Title == "" {
		say(lang, "statsEmpty")
		return
	}
	day := func(s string) string { // "2026-10-05T09:00:00" → "2026-10-05"
		if len(s) >= 10 {
			return s[:10]
		}
		return cmp.Or(s, "-")
	}
	stat := func(v, ai *int) string {
		if v == nil {
			return "-"
		}
		s := fmt.Sprintf("%3d %s", *v, scoreBar(*v))
		if ai != nil {
			s += fmt.Sprintf("  AI %d", *ai)
		}
		return s
	}
	drawDevType(d.Title)
	sayKV(lang, "statsType", d.Title, cache.Year, day(d.ComputedAt), cmp.Or(d.Description, "-"), cmp.Or(d.HashTags, "-"),
		cmp.Or(d.Position, "-"), stat(d.Agility, d.AgilityAI), stat(d.Stability, d.StabilityAI),
		stat(d.Contribution, d.ContribAI), stat(d.Adaptability, d.AdaptAI), stat(d.Consistency, d.ConsistAI),
		cmp.Or(d.TitleAI, "-"))
	if saved {
		notice(fmt.Sprintf(tr(lang, "statsSaved"), cache.FetchedAt.Local().Format("2006-01-02")))
	}
}

// devTypeArt is each developer type's picture as aline.team draws it, 11 squares wide: a letter is
// a square's color in devTypeColors, "." is empty. The icons are aline.team's and, like its name and
// logo, not part of the code's license (README).
var devTypeArt = map[string][]string{
	"BUILDER": {
		".gg........",
		"gggg.gg....",
		"gggggggg...",
		"....gggg...",
		"...........",
		"...gg..gg..",
		"..gggggggg.",
		"..gggggggg.",
	},
	"EXPLORER": {
		".....b.....",
		"....bbb....",
		"...bbbbb...",
		"...bbybb...",
		"...byyyb...",
		"...bbybb...",
		"...bbbbb...",
		"..bbbbbbb..",
		".bb.bbb.bb.",
		".b..y.y..b.",
		"....y.y....",
	},
	"FIXER": {
		"..s.....s..",
		"...s...s...",
		"...sssss...",
		"..sssssss..",
		"s.sysssys.s",
		".sssysysss.",
		"s.sssysss.s",
		".sssysysss.",
		"s.sysssys.s",
		"...sssss...",
	},
	"KEEPER": {
		"..sssssss..",
		".sssssssss.",
		".sssssssss.",
		".sssssswss.",
		".ssssswwss.",
		".swsswwsss.",
		".swwwwssss.",
		"..swwssss..",
		"..sswssss..",
		"...sssss...",
		"....sss....",
	},
	"LEADER": {
		"y....y....y",
		"yy..yyy..yy",
		"yyy.yyy.yyy",
		"yyyyyyyyyyy",
		"yyyyyyyyyyy",
		"yybyybyybyy",
		"yyyyyyyyyyy",
		"yyyyyyyyyyy",
	},
	"SPRINTER": {
		"....yyyyy..",
		"...yyyyy...",
		"...yyyy....",
		"..yyyy.....",
		"..yyyyyyyy.",
		".yyyyyyyy..",
		".....yyy...",
		"....yyy....",
		"...yyy.....",
		"..yy.......",
		".y.........",
	},
}

// devTypeColors are the xterm 256 colors nearest aline.team's (in the comments).
var devTypeColors = map[rune]int{
	'g': 77,  // #39D353
	'b': 68,  // #6699CC
	'y': 187, // #D9E0A3
	's': 108, // #7CB687
	'w': 255, // #E8F0EA
}

// drawDevType draws the type's picture above the result. A character is about twice as tall as wide,
// so each holds two squares, one above the other: "▀" in the upper square's color on the lower one's.
// Only in a terminal with color: without it the picture is gone.
func drawDevType(title string) {
	art := devTypeArt[title]
	if art == nil || !useColor {
		return
	}
	blank()
	for y := 0; y < len(art); y += 2 {
		var b strings.Builder
		for x, top := range art[y] {
			bottom := '.'
			if y+1 < len(art) {
				bottom = rune(art[y+1][x])
			}
			switch {
			case top == '.' && bottom == '.':
				b.WriteString(" ")
			case bottom == '.':
				b.WriteString(paint(fmt.Sprintf("38;5;%d", devTypeColors[top]), "▀"))
			case top == '.':
				b.WriteString(paint(fmt.Sprintf("38;5;%d", devTypeColors[bottom]), "▄"))
			default:
				b.WriteString(paint(fmt.Sprintf("38;5;%d;48;5;%d", devTypeColors[top], devTypeColors[bottom]), "▀"))
			}
		}
		fmt.Fprintln(out, strings.TrimRight(margin+"  "+b.String(), " "))
	}
}

// scoreBar draws a 0-100 score as ten blocks, the empty part dim.
func scoreBar(v int) string {
	n := min(max((v+5)/10, 0), 10)
	return strings.Repeat("█", n) + dim(strings.Repeat("░", 10-n))
}
