package main

import (
	"strings"
	"testing"
)

func TestAgentsFromEnv(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, ""},
		{map[string]string{"CLAUDECODE": "1", "AI_AGENT": "claude-code_2-1-283_agent"}, "claude-code"},
		{map[string]string{"CODEX_CI": "1"}, "codex"},
		{map[string]string{"AGENT": "amp"}, "amp"},
		{map[string]string{"OPENCODE": "1", "AGENT": "1"}, "opencode"},
		{map[string]string{"ROO_ACTIVE": "false", "CLAUDECODE": ""}, ""},
	} {
		got := strings.Join(agentsFromEnv(func(k string) string { return tc.env[k] }), ",")
		if got != tc.want {
			t.Errorf("agentsFromEnv(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestDetectAgents(t *testing.T) {
	for _, tc := range []struct{ name, email, msg, want string }{
		{"me", "me@x.com", "fix bug", ""},
		{"me", "me@x.com", "feat\n\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>", "claude-code"},
		{"me", "me@x.com", "feat\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)", "claude-code"},
		{"Codex", "codex@openai.com", "feat", "codex"},
		{"me (aider)", "me@x.com", "feat", "aider"},
		{"Copilot", "198982749+Copilot@users.noreply.github.com", "feat", "copilot"},
		{"bot", "opencode-agent[bot]@users.noreply.github.com", "feat", "opencode"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Dunn <72735688+Dunn-Kim@users.noreply.github.com>", ""},
		{"me", "me@x.com", "feat\n\nAssisted-by: OpenAI Codex", "codex"},
		{"me", "me@x.com", "feat\n\nMade-with: Cursor\nCo-authored-by: Cursor <cursoragent@cursor.com>", "cursor"},
		// As aline.team reads it: AI help with the message only is not AI help with the code.
		{"me", "me@x.com", "feat\n\nCommit-Message-Assisted-By: codex (via orca)", ""},
		{"me", "me@x.com", "feat\n\nCommit-Message-Assisted-By: claude (via claude code)", ""},
		{"me", "me@x.com", "feat\n\nCommit-Message-Assisted-By: opencode (gpt-5.5)", ""},
		{"me", "me@x.com", "feat\n\ncommit message written by claude", ""},
		{"me", "me@x.com", "feat\n\nCommit message generated with [Claude Code](https://claude.com/claude-code)", ""},
		{"me", "me@x.com", "feat\n\nCommit-Message-Made-With: Cursor", ""},
		{"me", "me@x.com", "feat\n\nCommit-Message-Assisted-By: claude (via claude code)\nCo-authored-by: Claude <noreply@anthropic.com>", "claude-code"},
		{"me", "me@x.com", "feat\n\nAssisted-by: opencode", "opencode"},
		{"me", "me@x.com", "feat\n\nAI-Assisted-By: Claude", "claude-code"},
		{"me", "me@x.com", "feat\n\nClaude-Session: 3f2a", "claude-code"},
		{"me", "me@x.com", "feat\n\nAgent-Logs-Url: https://github.com/o/r/agents/1", "copilot"},
		{"me", "me@x.com", "feat\n\nAmp-Thread-ID: T-1", ""},
		{"me", "me@x.com", "feat\n\nGenerated with Claude\n" + strings.Repeat("line\n", 20), ""}, // only the last 20 lines
	} {
		if got := strings.Join(detectAgents(tc.name, tc.email, tc.msg), ","); got != tc.want {
			t.Errorf("detectAgents(%q, %q, %q) = %q, want %q", tc.name, tc.email, tc.msg, got, tc.want)
		}
	}
}
