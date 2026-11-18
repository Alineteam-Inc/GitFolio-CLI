package main

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Agent signatures found in commit metadata (DESIGN 5.3): the authors the agents write themselves, as their
// docs, source code or commits show them. Add one line per new agent. In a Co-authored-by line only the
// wordTools' own addresses name a tool by themselves, as aline.team reads it; the others name it by the
// name or the domain beside them, or not at all ("Droid <droid@factory.ai>").
var agentEmails = map[string]string{
	"noreply@anthropic.com":       "claude-code",
	"noreply@openai.com":          "codex",
	"codex@openai.com":            "codex",
	"cursoragent@cursor.com":      "cursor",
	"copilot@github.com":          "copilot",
	"aider@aider.chat":            "aider",
	"amp@ampcode.com":             "amp",
	"agent@warp.dev":              "warp",
	"oz-agent@warp.dev":           "warp",
	"noreply@cognition.ai":        "devin",
	"droid@factory.ai":            "droid",
	"crush@charm.land":            "crush",
	"qwen-coder@alibabacloud.com": "qwen-code",
	"openhands@all-hands.dev":     "openhands",
	"vibe@mistral.ai":             "mistral-vibe",
	"noreply@codebuff.com":        "codebuff",
	"noreply@letta.com":           "letta-code",
	"noreply@continue.dev":        "continue",
	"agent@continue.dev":          "continue",
	"noreply@cline.bot":           "cline",
	"roomote@roocode.com":         "roo-code",
	"it+v0agent@vercel.com":       "v0",
}

// agentBots maps the GitHub apps agents commit as (<id>+<name>[bot]@users.noreply.github.com, or without
// the id) by name.
var agentBots = map[string]string{
	"claude": "claude-code", "anthropic-code-agent": "claude-code", "openai-code-agent": "codex",
	"chatgpt-codex-connector": "codex", "copilot-swe-agent": "copilot", "cursor": "cursor",
	"devin-ai-integration": "devin", "google-labs-jules": "jules", "gemini-code-assist": "gemini",
	"kiro-agent": "kiro", "opencode-agent": "opencode", "amazon-q-developer": "amazon-q", "oz-by-warp": "warp",
	"warp-factories": "warp", "factory-droid": "droid", "factory-ai": "droid", "augmentcode": "augment",
	"gpt-engineer-app": "lovable", "lovable-dev": "lovable", "kiloconnect": "kilo", "kilo-code-bot": "kilo",
	"cline-cloud": "cline", "roomote": "roo-code", "codegen-sh": "codegen", "continue": "continue",
	"sweep-ai": "sweep",
}

// agentAccounts maps the GitHub user accounts agents commit as by "<id>+<name>": by the id too, since a
// person may have the same name.
var agentAccounts = map[string]string{
	"198982749+copilot": "copilot", "175728472+copilot": "copilot", "223556219+copilot": "copilot",
	"242468646+claude": "claude-code", "242516109+codex": "codex", "244629292+kiro-agent": "kiro",
	"247260674+junie-agent": "junie",
}

// AI use a commit message tells, read as aline.team reads it (its commit trailer parser), in the last
// 20 non-blank lines: a Co-authored-by line naming an AI tool, a footer whose verb (generated with or by,
// assisted-by, written by, created with, made-with, ...) comes before one, or a session trailer an agent
// leaves. A verb right after "message" ("Commit-Message-Assisted-By: ...") says only the message was
// written with AI, not the code, and does not count. A tool is named by a word of its own ("Haider" is
// not aider), and a person's name ("Devin Lee") counts only with an agent's address.
var (
	coAuthorLine   = regexp.MustCompile(`(?i)^\s*co-?authored[-\s]by\s*:?\s*(.*?)\s*(?:<([^<>]*)>)?\s*$`)
	aiFooter       = regexp.MustCompile(`(?i)(generated\s+(?:with|by)|assisted[-\s]by|co-?authored[-\s]by|written\s+by|created\s+(?:with|by)|made[-\s]with)[:\s]+\[?\s*([a-z0-9 ._\-]{0,30})`)
	sessionTrailer = regexp.MustCompile(`(?im)^\s*(claude-session|agent-logs-url|amp-thread-id|copilot-session)\s*:|^\s*(replit)-commit-author\s*:\s*agent\s*$`)
)

// sessionAgents maps the session trailers agents leave to the agent: the trailer's name says which.
var sessionAgents = map[string]string{"claude-session": "claude-code", "agent-logs-url": "copilot", "amp-thread-id": "amp",
	"copilot-session": "copilot", "replit": "replit"}

// aiVendors are the AI tools aline.team looks for, in its order, with GitFolio's agent IDs; '-' parts
// a name into words.
var aiVendors = []struct{ name, agent string }{
	{"claude", "claude-code"}, {"copilot", "copilot"}, {"cursor", "cursor"}, {"codex", "codex"}, {"aider", "aider"},
	{"gemini-code", "gemini"}, {"devin", "devin"}, {"sweep-ai", "sweep"}, {"opencode", "opencode"}, {"ampcode", "amp"},
	{"warp", "warp"}, {"factory-droid", "droid"}, {"crush", "crush"}, {"qwen", "qwen-code"}, {"openhands", "openhands"},
	{"mistral-vibe", "mistral-vibe"}, {"codebuff", "codebuff"}, {"letta", "letta-code"}, {"continue", "continue"},
	{"sisyphus", "sisyphus"}, {"google-labs-jules", "jules"}, {"kilo-code", "kilo"}, {"cline", "cline"},
	{"roo-code", "roo-code"},
}

// personalTools are the tools whose names are also people's: a name naming only them may be a person's.
var personalTools = []string{"claude-code", "devin", "letta-code", "cline"}

// wordTools are the tools whose names are also everyday words ("to continue the migration"): one names
// the tool only as the first word of a name or a footer's phrase, or by its own address (agentEmails),
// never by an email's domain.
var wordTools = []string{"warp", "crush", "continue"}

var (
	// vendorSuffixes make one word with a tool's name and still name the tool ("claudecode", "cursoragent").
	vendorSuffixes = []string{"code", "agent", "bot", "ai", "cli", "app"}
	// notPersonWords in a name make it a tool's or a sentence's, not a person's.
	notPersonWords = []string{"code", "agent", "agents", "ai", "bot", "cli", "app", "assistant", "autofix", "chat", "model",
		"opus", "sonnet", "haiku", "fable", "gpt", "composer", "workspace", "desktop", "coding", "swe", "integration", "ide",
		"web", "and", "the", "for", "to", "in", "of", "with", "by", "via", "from", "on", "at", "a", "an", "is", "was",
		"openai", "anthropic", "github", "google", "microsoft"}
)

func agentByEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if a, ok := agentEmails[email]; ok {
		return a
	}
	if local, ok := strings.CutSuffix(email, "@users.noreply.github.com"); ok {
		if a, ok := agentAccounts[local]; ok {
			return a
		}
		if _, name, found := strings.Cut(local, "+"); found {
			local = name
		}
		if name, ok := strings.CutSuffix(local, "[bot]"); ok { // an app: no person's account ends so
			return agentBots[name]
		}
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
	var rest strings.Builder // the lines but Co-authored-by ones, which the footer rule must not take past the name rule
	for _, line := range strings.Split(tailLines(message, 20), "\n") {
		if m := coAuthorLine.FindStringSubmatch(line); m != nil {
			for _, a := range vendorsOf(m[1], m[2], true) {
				add(a)
			}
			line = ""
		}
		rest.WriteString(line + "\n")
	}
	tail := rest.String()
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
		verb := strings.ToLower(tail[pos+m[2] : pos+m[3]])
		for _, a := range vendorsOf(tail[pos+m[4]:pos+m[5]], "", !strings.HasSuffix(verb, "with")) { // "...with" names a tool
			add(a)
		}
		pos += m[1]
	}
	for _, m := range sessionTrailer.FindAllStringSubmatch(tail, -1) {
		add(sessionAgents[strings.ToLower(m[1]+m[2])])
	}
	slices.Sort(agents) // map iteration order is random; keep output stable
	return agents
}

// vendorsOf is the AI tools a co-author's name and email, or a footer's phrase, name: from an agent's
// address, those its name and address name; otherwise those its name names, unless it may be a person's
// name, names only personalTools, and looks like one.
func vendorsOf(name, email string, mayBePerson bool) []string {
	words := wordsOf(name)
	found := slices.DeleteFunc(vendorsIn(words), func(a string) bool {
		return slices.Contains(wordTools, a) && !slices.Contains(vendorsIn(words[:1]), a)
	})
	if strings.Index(email, "@") > 0 && agentAddress(email) { // a masked "[EMAIL]" is no address
		for _, a := range vendorsIn(wordsOf(email)) {
			if !slices.Contains(found, a) && !slices.Contains(wordTools, a) {
				found = append(found, a)
			}
		}
		if a := wordToolEmail(email); a != "" && !slices.Contains(found, a) {
			found = append(found, a)
		}
		return found
	}
	// Only a few tools' names are also people's (personalTools): with another tool's name it is the tool ("Codex Worker").
	personal := !slices.ContainsFunc(found, func(a string) bool { return !slices.Contains(personalTools, a) })
	if len(found) == 0 || mayBePerson && personal && looksLikePerson(name, words) {
		return nil
	}
	return found
}

// wordsOf is the runs of letters and digits in text, lowercased.
func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// vendorsIn is the agent IDs of the AI tools named by a word of words: the name itself, the name and a
// suffix of vendorSuffixes or digits ("claude3"), or a name with '-' as consecutive words or one word.
func vendorsIn(words []string) []string {
	var found []string
	for _, v := range aiVendors {
		parts := strings.Split(v.name, "-")
		joined := strings.Join(parts, "")
		for i, w := range words {
			tail, ok := strings.CutPrefix(w, joined)
			if ok && (tail == "" || slices.Contains(vendorSuffixes, tail) || strings.Trim(tail, "0123456789") == "") ||
				len(parts) > 1 && i+len(parts) <= len(words) && slices.Equal(words[i:i+len(parts)], parts) {
				found = append(found, v.agent)
				break
			}
		}
	}
	return found
}

// wordToolEmail is the wordTool whose own address email is ("Oz <oz-agent@warp.dev>"), or "".
func wordToolEmail(email string) string {
	if a := agentEmails[strings.ToLower(strings.TrimSpace(email))]; slices.Contains(wordTools, a) {
		return a
	}
	return ""
}

// agentAddress reports whether email is an address agents use: a GitHub app's ([bot]), an AI company's,
// a wordTool's own, one whose domain names a tool other than an everyday word's (cursor.com), or a placeholder (example.com, *.local). A GitHub noreply
// address's account name is no sign: people have accounts named devin or claude.
func agentAddress(email string) bool {
	email = strings.ToLower(email)
	at := strings.LastIndex(email, "@")
	domain := strings.TrimSpace(email[at+1:])
	return strings.Contains(email[:at], "[bot]") || domain == "anthropic.com" || domain == "openai.com" || wordToolEmail(email) != "" ||
		domain == "localhost" || strings.HasSuffix(domain, ".local") || strings.HasSuffix(domain, ".invalid") ||
		strings.HasSuffix(domain, ".test") || domain == "example.com" || domain == "example.org" || domain == "example.net" ||
		slices.ContainsFunc(vendorsIn(wordsOf(domain)), func(a string) bool { return !slices.Contains(wordTools, a) })
}

// looksLikePerson reports whether a name is shaped like a person's: two to four words of letters only,
// none of notPersonWords, and one that names no tool ("Devin Lee", "Claude Monet"; not "Claude Opus 5.5",
// "Cursor Agent" or "Claude").
func looksLikePerson(raw string, words []string) bool {
	if len(words) < 2 || len(words) > 4 || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsDigit(r) || strings.ContainsRune("()[]{}<>/@", r) }) {
		return false
	}
	other := false
	for _, w := range words {
		if slices.Contains(notPersonWords, w) {
			return false
		}
		other = other || len(vendorsIn([]string{w})) == 0
	}
	return other
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
