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
		{"me", "me@x.com", "feat\n\nAmp-Thread-ID: https://ampcode.com/threads/T-1", "amp"},
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
		// What the agents write themselves (checked 2026-11-18).
		{"me", "me@x.com", "feat\n\nCo-Authored-By: Warp <agent@warp.dev>", "warp"},
		{"me", "me@x.com", "feat\n\nCo-Authored-By: Oz <oz-agent@warp.dev>", "warp"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: factory-droid[bot] <138933559+factory-droid[bot]@users.noreply.github.com>", "droid"},
		{"me", "me@x.com", "feat\n\n💘 Generated with Crush\n\nAssisted-by: Crush:claude-sonnet-4-5", "crush"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Qwen-Coder <qwen-coder@alibabacloud.com>", "qwen-code"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: openhands <openhands@all-hands.dev>", "openhands"},
		{"me", "me@x.com", "feat\n\nCo-Authored-By: Mistral Vibe <vibe@mistral.ai>", "mistral-vibe"},
		{"me", "me@x.com", "feat\n\n🤖 Generated with Codebuff\nCo-Authored-By: Codebuff <noreply@codebuff.com>", "codebuff"},
		{"me", "me@x.com", "feat\n\nCo-Authored-By: Letta Code <noreply@letta.com>", "letta-code"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Letta Smith <letta@corp.com>", ""}, // a person's name, like Devin's
		{"me", "me@x.com", "feat\n\nGenerated with [Continue](https://continue.dev)\nCo-Authored-By: Continue <noreply@continue.dev>", "continue"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: sisyphus-dev-ai <sisyphus-dev-ai@users.noreply.github.com>", "sisyphus"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: google-labs-jules[bot] <161369871+google-labs-jules[bot]@users.noreply.github.com>", "jules"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Jules Verne <jules@corp.com>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: kilo-code-bot[bot] <240665456+kilo-code-bot[bot]@users.noreply.github.com>", "kilo"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Cline <noreply@cline.bot>", "cline"},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Roo Code <roomote@roocode.com>", "roo-code"},
		{"me", "me@x.com", "feat\n\nCopilot-Session: 0f3a", "copilot"},
		{"me", "me@x.com", "feat\n\nReplit-Commit-Author: Agent\nReplit-Commit-Session-Id: 1", "replit"},
		{"me", "me@x.com", "feat\n\nReplit-Commit-Author: User", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Codeium <a@b.com>", ""}, // no agent writes it
		// A surname, and everyday words: only as the first word, or by the tool's own email.
		{"me", "me@x.com", "feat\n\nCo-authored-by: Bob Cline <bob@corp.com>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Cline Agent <cline-agent@users.noreply.github.com>", "cline"},
		{"me", "me@x.com", "feat\n\nCreated by me to continue the migration", ""},
		{"me", "me@x.com", "feat\n\nmade with love and warp drive", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: John Warp <john@corp.com>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Bob Smith <bob@continue-education.org>", ""},
		{"me", "me@x.com", "feat\n\nCo-authored-by: Bob Smith <bob@crush.dev>", ""},
		// Agents as the commit's author: GitHub apps by name, accounts by id and name.
		{"Devin AI", "158243242+devin-ai-integration[bot]@users.noreply.github.com", "feat", "devin"},
		{"claude[bot]", "41898282+claude[bot]@users.noreply.github.com", "feat", "claude-code"},
		{"Claude", "242468646+Claude@users.noreply.github.com", "feat", "claude-code"},
		{"Codex", "242516109+Codex@users.noreply.github.com", "feat", "codex"},
		{"Kiro Agent", "244629292+kiro-agent@users.noreply.github.com", "feat", "kiro"},
		{"augment", "185243770+augmentcode[bot]@users.noreply.github.com", "feat", "augment"},
		{"v0", "it+v0agent@vercel.com", "feat", "v0"},
		{"Copy Lot", "123+copilot@users.noreply.github.com", "feat", ""}, // a person's account named copilot
		{"Cursor", "456+cursor@users.noreply.github.com", "feat", ""},
	} {
		if got := strings.Join(detectAgents(tc.name, tc.email, tc.msg), ","); got != tc.want {
			t.Errorf("detectAgents(%q, %q, %q) = %q, want %q", tc.name, tc.email, tc.msg, got, tc.want)
		}
	}
}
