package main

import (
	"strings"
	"testing"
)

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
		{"me", "me@x.com", "feat\n\nAssisted-by: OpenAI Codex", "codex"}, // a company's name is no person's
		{"me", "me@x.com", "feat\n\nCo-authored-by: GitHub Copilot <[EMAIL]>", "copilot"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: GitHub Copilot", "copilot"},
		{"me", "me@x.com", "feat\n\nWritten by Anthropic Claude", "claude-code"},
		{"me", "me@x.com", "feat\n\nCreated by Microsoft Copilot", "copilot"},
		{"me", "me@x.com", "feat\n\nAssisted-by: Google Gemini Code Assist", "gemini"},
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
		// A tool is named by a word of its own, and a person's name counts only with an agent's address.
		{"me", "me@x.com", "feat\n\nCo-authored-by: Zain Haider <123+zainhaider@users.noreply.github.com>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Devin Lee <devin@corp.com>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Claude Monet <claude.monet@example.fr>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Claude Opus 5.5", "claude-code"},
		{"me", "me@x.com", "feat\n\n  co-authored-by Claude <[EMAIL]>", "claude-code"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Cursor Agent <agent@cursor.sh>", "cursor"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Amp <amp@ampcode.com>", "amp"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Warp <agent@warp.dev>", "warp"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Copilot Autofix powered by AI <62310815+github-advanced-security[bot]@users.noreply.github.com>", "copilot"},
		{"me", "me@x.com", "feat\n\nWritten by Haider", ""},
		{"me", "me@x.com", "feat\n\nCreated by Devin Lee", ""},
		{"me", "me@x.com", "feat\n\nAssisted-by: Claudette", ""},
		{"me", "me@x.com", "feat\n\nmade with precursor tools", ""},
		{"me", "me@x.com", "feat\n\nGenerated with Claude and reviewed by me", "claude-code"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Gemini Code Assist <gemini@example.com>", "gemini"},
		// Only claude and devin are also people's names: with another tool's name it is the tool.
		{"me", "me@x.com", "feat\n\nCo-authored-by: Codex Worker", "codex"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Codex Worker <[EMAIL]>", "codex"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Copilot Reviewer <rev@corp.com>", "copilot"},
		{"me", "me@x.com", "feat\n\nWritten by Cursor Team", "cursor"},
		{"me", "me@x.com", "feat\n\nAssisted-by: Opencode Zen", "opencode"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Jean Claude Van Damme <jcvd@corp.com>", ""},
	} {
		if got := strings.Join(detectAgents(tc.name, tc.email, tc.msg), ","); got != tc.want {
			t.Errorf("detectAgents(%q, %q, %q) = %q, want %q", tc.name, tc.email, tc.msg, got, tc.want)
		}
	}
}
