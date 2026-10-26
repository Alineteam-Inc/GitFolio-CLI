---
name: sync-doctor
description: Find out why the user's commits or statistics are not on aline.team, using GitFolio's own status, and say what to run. Use when the user says their profile is missing commits, a repository or recent work.
allowed-tools: mcp__plugin_gitfolio_gitfolio__status, Bash(gitfolio status *), Bash(gitfolio list), Bash(gitfolio sync --dry-run), Bash(gitfolio config), Bash(gitfolio email), Bash(gitfolio schedule)
---

# Sync doctor

GitFolio collects the user's own pushed commits on this computer and sends them to aline.team. When the
profile looks behind, find which step stopped, then tell the user what to run. Only read: never send,
remove, purge or change settings yourself.

## Steps

1. Call the GitFolio `status` tool (or run `gitfolio status`). Look at `loggedIn`, `syncMode`,
   `lastSync`, `lastSyncError`, and per repository `commits` against `onAlineTeam` and
   `waitingForVerifiedEmail`.
2. Match what you see:
   - `loggedIn` false: nothing is sent; use the `setup` skill (or `gitfolio login`).
   - `waitingForVerifiedEmail`: aline.team takes that repository only after its email is verified.
     `gitfolio email` shows which email; verify it with `gitfolio email verify <email>`, or send the
     code with `email_start` if the user wants you to (they enter it in the app or a terminal, not here).
   - `syncMode` is `local`: nothing is sent by itself; statistics go when the user asks for their type
     or portfolio, or runs `gitfolio sync`. `gitfolio config autosync on` sends after every push.
   - Commits here but fewer on aline.team, no error: run `gitfolio sync --dry-run` to see what would go,
     then suggest `gitfolio sync`.
   - `lastSyncError` set: quote it and explain it.
   - A repository missing from `status`: it is not registered (`gitfolio add <path>`), or hidden from AI
     tools (`gitfolio config ai`). One with no commits: check `gitfolio list` (hooks), that the commits
     are pushed, and that they were made with one of the user's emails (`gitfolio email`).
3. Give the user the commands to run, one per line, and why. Ask before suggesting anything that deletes
   data (`gitfolio remove --purge`).
