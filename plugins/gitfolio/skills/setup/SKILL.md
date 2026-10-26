---
name: setup
description: Start GitFolio for the user or change what it shares - log in with their email, register repositories, choose the sync mode, and set what AI tools may see of each repository following the codebase's rules. Use when the user wants to start GitFolio, or when GitFolio's status says it is not logged in.
allowed-tools: mcp__plugin_gitfolio_gitfolio__status, mcp__plugin_gitfolio_gitfolio__policy, mcp__plugin_gitfolio_gitfolio__ai_settings
---

# Set up GitFolio

GitFolio collects the metadata of the user's own pushed commits (never source code) and aline.team works
out a developer type and tech stack from it. You set it up with the user. Every choice below is theirs:
ask, and never guess an email, a code or a repository.

## Steps

1. **Check.** Call the GitFolio `status` tool. Without GitFolio's tools, the GitFolio CLI is missing or old:
   point the user to https://github.com/Alineteam-Inc/GitFolio-CLI#install and stop.
2. **Policy and actions.** Call `policy` and show the user, in their language, before doing anything:
   - what is collected and where it goes (`dataPolicy`), with the links to the terms of service and the
     privacy policy;
   - every action starting GitFolio takes (`setupActions`): an account for a new email, git hooks in the
     chosen repositories, data kept on this computer, what is sent and when, a daily sync in the
     scheduler, what AI tools see.
   Go on only when the user agrees. Before each later step that does one of these, say so again in a line.
3. **Email (required).** Ask for the email to use. You may mention the git email of this repository, but
   the user decides. Ask whether they want aline.team service emails (no unless they say yes). Call
   `login_start` with `agreed: true`, then ask for the code from the email and call `login_verify`.
4. **Repositories.** Ask which repositories to register (suggest the current one). Tell them each gets git
   hooks (post-commit, pre-push) in this computer's `.git` folder only: nothing is added to the repository
   or its remote, they never stop a commit or push, hooks they have still run first, and `gitfolio remove`
   undoes it. Call `add_repository` for each. If a repository's git email is not the account's, offer
   `email_start` and `email_verify`: aline.team takes commits only under verified emails.
5. **What AI tools may see**, for each repository:
   - First read the codebase's own rules: `CLAUDE.md`, `AGENTS.md`, `.cursor/rules`, `CONTRIBUTING`,
     security or confidentiality notes. Note anything about company or customer names, confidential
     projects, or sharing commit messages and branch names.
   - Propose settings from what you found and ask the user: name (real by default, anonymous, or another
     name), commit messages (off by default, subjects or full), branch names (off by default), or hidden.
   - Where the rules and the user's wish differ, tell the user and keep the stricter one unless they
     clearly choose otherwise.
   - Apply with `set_ai_exposure`. File paths, folders and file names are never shown, whatever is set.
6. **Sync with aline.team?** GitFolio is in local mode by default: nothing goes out on its own, and it
   calls the aline.team API, with daily totals, only when the user looks up their type or portfolio. Ask whether they want automatic sync
   instead (the past commits right away, then after each git push, to keep their aline.team profile
   current). Call `set_sync_mode` only if they choose automatic.
7. **Summary.** List what was set and how to change it later: `gitfolio config ai`, `gitfolio config
   autosync on|off`, `gitfolio email`.

A person can do all of this without an AI tool: `gitfolio init`, `gitfolio config ai`.
