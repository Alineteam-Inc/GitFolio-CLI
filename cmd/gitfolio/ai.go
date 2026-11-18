package main

import (
	"regexp"
	"slices"
	"strings"
)

// Agent signatures found in commit metadata (DESIGN 5.3). Add one line per new agent.
var agentEmails = map[string]string{
	"noreply@anthropic.com":  "claude-code",
	"noreply@openai.com":     "codex",
	"codex@openai.com":       "codex",
	"cursoragent@cursor.com": "cursor",
	"copilot@github.com":     "copilot",
	"aider@aider.chat":       "aider",
	"amp@ampcode.com":        "amp",
	"agent@warp.dev":         "warp",
	"oz-agent@warp.dev":      "warp",
}

// githubBots maps the name part of <id>+<name>@users.noreply.github.com (with "[bot]" removed).
var githubBots = map[string]string{
	"copilot":              "copilot",
	"copilot-swe-agent":    "copilot",
	"cursor":               "cursor",
	"devin-ai-integration": "devin",
	"google-labs-jules":    "jules",
	"kiro-agent":           "kiro",
	"opencode-agent":       "opencode",
	"amazon-q-developer":   "amazon-q",
}

// AI use a commit message tells, read as aline.team reads it (its commit trailer parser), in the last
// 20 non-blank lines: a Co-authored-by trailer naming an AI vendor, a footer whose verb (generated
// with, assisted-by, written by, created with, made-with, ...) comes before one, or a session trailer
// an agent leaves. A verb right after "message" ("Commit-Message-Assisted-By: ...") says only the message
// was written with AI, not the code, and does not count.
var (
	coAuthorTrailer = regexp.MustCompile(`(?im)^Co-Authored-By:\s+(.+?)\s*<(.+?)>\s*$`)
	aiFooter        = regexp.MustCompile(`(?i)(?:generated\s+with|assisted[-\s]by|co-?authored[-\s]by|written\s+by|created\s+(?:with|by)|made[-\s]with)[:\s]+\[?\s*([a-z0-9 ._\-]{0,30})`)
	sessionTrailer  = regexp.MustCompile(`(?im)^\s*(claude-session|agent-logs-url)\s*:`)
	vendorNoise     = regexp.MustCompile(`[\s._\-\[\]<>()]`)
)

// aiVendors maps the AI vendors aline.team looks for in those to GitFolio's agent IDs.
var aiVendors = map[string]string{
	"claude": "claude-code", "copilot": "copilot", "cursor": "cursor", "codex": "codex", "aider": "aider",
	"codeium": "codeium", "gemini-code": "gemini", "chatgpt": "chatgpt", "devin": "devin", "sweep-ai": "sweep",
	"opencode": "opencode", "ampcode": "amp",
}

func agentByEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if a, ok := agentEmails[email]; ok {
		return a
	}
	if local, ok := strings.CutSuffix(email, "@users.noreply.github.com"); ok {
		if _, name, found := strings.Cut(local, "+"); found {
			local = name
		}
		return githubBots[strings.TrimSuffix(local, "[bot]")]
	}
	return ""
}

// detectAgents returns the AI agents that the commit's author, trailers or message markers point to.
// It must run on the raw message, before masking hides trailer emails.
func detectAgents(authorName, authorEmail, message string) []string {
	var agents []string
	add := func(a string) {
		if a != "" && !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
	}
	add(agentByEmail(authorEmail))
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(authorName)), "(aider)") {
		add("aider")
	}
	vendors := func(text string) {
		text = vendorNoise.ReplaceAllString(strings.ToLower(text), "")
		for v, a := range aiVendors {
			if strings.Contains(text, vendorNoise.ReplaceAllString(v, "")) {
				add(a)
			}
		}
	}
	tail := tailLines(message, 20)
	for _, m := range coAuthorTrailer.FindAllStringSubmatch(tail, -1) {
		vendors(m[1])
		vendors(m[2])
		add(agentByEmail(m[2])) // agents aline.team knows no vendor name for
	}
	for pos := 0; ; {
		m := aiFooter.FindStringSubmatchIndex(tail[pos:])
		if m == nil {
			break
		}
		start, before := pos+m[0], tail[:pos+m[0]]
		if n := len(before); n >= 8 && strings.ContainsRune("- \t\n\v\f\r", rune(before[n-1])) && strings.EqualFold(before[n-8:n-1], "message") {
			pos = start + 1 // only the message was written with AI
			continue
		}
		vendors(tail[pos+m[2] : pos+m[3]])
		pos += m[1]
	}
	for _, m := range sessionTrailer.FindAllStringSubmatch(tail, -1) {
		add(map[bool]string{true: "claude-code", false: "copilot"}[strings.EqualFold(m[1], "claude-session")])
	}
	slices.Sort(agents) // map iteration order is random; keep output stable
	return agents
}

// tailLines is the end of s from its maxLines-th last non-blank line.
func tailLines(s string, maxLines int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	start, n := 0, 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			n++
		}
		if n >= maxLines {
			start = i
			break
		}
	}
	return strings.Join(lines[start:], "\n")
}

// coAuthorEmails returns the lowercased emails of the message's Co-authored-by trailers.
func coAuthorEmails(message string) []string {
	var emails []string
	for _, line := range strings.Split(message, "\n") {
		v, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(line)), "co-authored-by:")
		if !ok {
			continue
		}
		if i, j := strings.LastIndex(v, "<"), strings.LastIndex(v, ">"); i >= 0 && j > i {
			emails = append(emails, strings.TrimSpace(v[i+1:j]))
		}
	}
	return emails
}

// How a collected commit was made (DESIGN 5.1).
const (
	creationHuman     = "HUMAN"       // authored by the user, no AI signal
	creationHumanCoAI = "HUMAN_CO_AI" // authored by the user with an AI agent involved
	creationAICoHuman = "AI_CO_HUMAN" // authored by an AI agent with the user as co-author
)

// creationType tells whether c belongs to the user and how it was made; "" means it is not the user's.
// An agent's commit without the user as co-author cannot be attributed to anyone, so it is skipped.
func creationType(c Commit, mine map[string]bool) string {
	if mine[strings.ToLower(c.AuthorEmail)] {
		if len(c.AIAgents) > 0 {
			return creationHumanCoAI
		}
		return creationHuman
	}
	if agentByEmail(c.AuthorEmail) != "" {
		for _, e := range c.coAuthors {
			if mine[e] {
				return creationAICoHuman
			}
		}
	}
	return ""
}
