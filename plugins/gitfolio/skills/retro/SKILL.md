---
name: retro
description: Draft the user's stand-up update or their weekly or monthly retrospective from their own commits in GitFolio. Use when the user asks what they did yesterday, this week or this month, for a stand-up, a retro, a status report or a work log.
allowed-tools: mcp__plugin_gitfolio_gitfolio__status, mcp__plugin_gitfolio_gitfolio__work, mcp__plugin_gitfolio_gitfolio__timeline, mcp__plugin_gitfolio_gitfolio__commits, mcp__plugin_gitfolio_gitfolio__ai_settings
---

# Stand-up and retrospective

GitFolio holds the metadata of the user's own pushed commits, and their messages only for repositories
where the user lets AI tools see them. Write from those facts, in the user's language, and never add work
they do not show.

## Steps

1. **Check.** Call `status`. No repositories: use the `setup` skill first. GitFolio collects commits when
   they are pushed, so work not pushed yet is not there: say so if the period is recent.
2. **Period.** Take it from the request, or ask:
   - stand-up: since the last working day (on a Monday, since Friday);
   - weekly: the last seven days, or this week;
   - monthly: the month.
   The tools take `from` and `to` as a year, month or day (`2026-11-03`) in local time; `to` includes
   that day.
3. **Facts.**
   - `work` with the period: per repository, commits, commit types, kinds of work, languages, lines and
     the share made with AI.
   - `commits` with the period, for each repository with commits whose messages are shown (`ai_settings`):
     the subjects, and the bodies where the user shows full messages. These are the actual items; take
     the next page with `nextOffset`.
   - Monthly: `timeline` for the months before, to show what changed.
   - A repository without messages gives counts only: ask the user what that work was, or leave it as
     counts.
4. **Write.**
   - Stand-up: done (grouped by repository or theme, from the subjects), next and blockers. Ask the user
     for the last two; never guess them.
   - Retrospective: highlights (features and fixes by theme), the numbers (commits, repositories, kinds of
     work, share made with AI), what went well and what to improve. Take the last two from the user's
     answers.
   - Keep the numbers as the tools give them, and the repositories by the names the tools use (an alias
     or "Repository N" stays so). No hashes, paths or file names.
5. Offer a shorter version for a chat message, or to save it as Markdown.
