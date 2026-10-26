# GitFolio plugin for Claude Code and Codex

Let your AI coding tool write your portfolio, resume or tech stack career from your own git history:
GitFolio gives it the facts of your work, you give it what the work was about, and it writes only from
those two. Through the [GitFolio CLI](https://github.com/Alineteam-Inc/GitFolio-CLI).

## What it adds

**MCP server `gitfolio`** (`gitfolio mcp`):

- To read, as you allow (below): `status` (repositories here, login, sync), `work` (per repository and
  period: commit types, kinds of work, languages, lines, share made with AI), `timeline` (month by month),
  `commits` (only where you show messages), `stack` (tech stack periods), `devtype` (your type, scores
  and aline.team's reasoning), `portfolio` (a Markdown draft), `ai_settings`.
- To set GitFolio up for you: `policy`, `login_start` and `login_verify` (your email and its code, which
  you can also enter in the GitFolio app or a terminal), `email_start` (a work email: its code is entered
  in the app or a terminal, not in the conversation), `add_repository`, `set_sync_mode`, `set_ai_exposure`.
  Your AI tool asks before each. Nothing can be removed or deleted through it.

**Skills:** `setup` (start GitFolio and decide what to share), `portfolio` (a short interview, then your
portfolio, resume or career description, fitted to a job posting if you give one), `tech-career`,
`devtype` (your scores explained), `sync-doctor` (why something is missing on aline.team).

## Needs

The GitFolio CLI 0.7.0 or later on your `PATH`. The plugin does not install it: see
[Install](https://github.com/Alineteam-Inc/GitFolio-CLI#install). The `setup` skill logs you in with your
email if you are not yet.

## Install

Claude Code:

```sh
/plugin marketplace add Alineteam-Inc/GitFolio-CLI
/plugin install gitfolio@alineteam
```

Codex:

```sh
codex plugin marketplace add Alineteam-Inc/GitFolio-CLI
codex plugin add gitfolio@alineteam
```

Any other tool that runs MCP servers: add the command `gitfolio mcp`.

## What goes where

- **What AI tools see** is set per repository (`gitfolio config ai`, or `set_ai_exposure` when you or your
  codebase's rules say so): the repository's real name (default), anonymous ("Repository 1") or another
  name — then its address is left out too; commit messages off (default), subjects or full; branch names
  off (default) or on; or hidden. File paths, folder and file names, commit hashes and emails are never
  shown: kinds of work are worked out on your computer and only their shares leave it.
- What a tool returns becomes part of the conversation, so your AI tool's provider receives it like
  anything else you type.
- `devtype` and `portfolio` ask aline.team for the developer type, as `gitfolio stats` does; in local
  mode they first run the sync it runs. `stack` is worked out on this computer, as `gitfolio stack` is. Logging in and adding a repository work as
  `gitfolio login` and `gitfolio add` do. What the CLI sends to aline.team is listed in its
  [README](https://github.com/Alineteam-Inc/GitFolio-CLI#what-leaves-your-computer).

Apache-2.0, like GitFolio. GitFolio and aline.team are Alineteam Inc.'s names.
