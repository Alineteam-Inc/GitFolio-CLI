package main

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// maskRules run in order: secrets first (they can sit inside URLs), then URLs (they can contain "@"),
// then emails, IPs and issue references.
var maskRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// A private key goes whole, to its END line or the end of the message; masking before maskVersion 1
	// replaced only its BEGIN line, so what that left of one goes too.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY[A-Z ]*-----.*?(?:-----END [A-Z ]*PRIVATE KEY[A-Z ]*-----|\z)`), "[SECRET]"},
	{regexp.MustCompile(`(?s)\[SECRET\][^\[]*?-----END [A-Z ]*PRIVATE KEY[A-Z ]*-----`), "[SECRET]"},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_\w{20,}|glpat-[\w-]{20,}|npm_[A-Za-z0-9]{36}|AIza[\w-]{35}|[rs]k_live_[A-Za-z0-9]{20,}|sk-[\w-]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[\w-]{10,}|eyJ[\w-]{10,}\.[\w-]{10,}\.[\w-]{10,})`), "[SECRET]"},
	{regexp.MustCompile(`\baln_cli_[A-Za-z0-9_-]{20,}`), "[SECRET]"}, // aline.team CLI token
	{regexp.MustCompile(`(?i)\b(?:https?|ssh|git|ftp)://[^\s<>()\[\]"']+|\bwww\.[^\s<>()\[\]"']+`), "[URL]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`), "[EMAIL]"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[IP]"},
	{regexp.MustCompile(`\B#\d+\b`), "[TICKET]"},
}

// ticketRe matches issue keys such as PAY-123. Standard names that look the same (UTF-8, SHA-256) are kept.
// ponytail: fixed allowlist; add prefixes here when real messages get masked by mistake.
var ticketRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)

var notTickets = map[string]bool{
	"UTF": true, "SHA": true, "ISO": true, "RFC": true, "HTTP": true, "TLS": true, "SSL": true, "ES": true,
	"MD": true, "AES": true, "RSA": true, "CVE": true, "PEP": true, "JSR": true, "ECMA": true, "GPT": true, "IPV": true,
}

// maskVersion counts the changes to maskRules that must reach what was stored before: when it grows, the
// stored messages are masked again once (scanRepos), and the next sync sends the ones that changed.
const maskVersion = 1

// masker hides sensitive values before anything is stored or sent (DESIGN 4).
type masker struct{ words *regexp.Regexp }

// newMasker builds a masker for the user's blocked words (customer or internal project names).
func newMasker(words []string) masker {
	var parts []string
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			parts = append(parts, regexp.QuoteMeta(w))
		}
	}
	if len(parts) == 0 {
		return masker{}
	}
	slices.SortFunc(parts, func(a, b string) int { return cmp.Compare(len(b), len(a)) }) // longest first
	return masker{regexp.MustCompile(`(?i)` + strings.Join(parts, "|"))}
}

func (m masker) apply(s string) string {
	for _, r := range maskRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	s = ticketRe.ReplaceAllStringFunc(s, func(k string) string {
		if notTickets[k[:strings.IndexByte(k, '-')]] {
			return k
		}
		return "[TICKET]"
	})
	if m.words != nil {
		s = m.words.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// commit masks the sensitive parts of c's message, the only text GitFolio masks (DESIGN 4); file,
// repository and branch names are kept as they are. AI detection must already have run on the raw message.
func (m masker) commit(c *Commit) { c.Message = m.apply(c.Message) }
