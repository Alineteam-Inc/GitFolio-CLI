# GitFolio

**Your development work, written up as a portfolio — from your own git history, without sharing a line of code.**

[Latest release](https://github.com/Alineteam-Inc/GitFolio-CLI/releases/latest) · [한국어](#한국어)

## What GitFolio is

Most of a developer's best work sits in private or company repositories that nobody outside can look
at. GitFolio turns that history into facts you can use: what you worked on, for how long, in which
languages and frameworks, what kind of work it was, and how much of it you did with AI.

- **A command line tool** (macOS, Linux, Windows) by [Alineteam Inc.](https://aline.team). It reads the
  metadata of the commits you authored and pushed, on your computer — never source code.
- **With [aline.team](https://aline.team)'s API** it works out your developer type (five scores, with and
  without AI) and how long you used each technology.
- **With your AI coding tool** (Claude Code, Codex and others that run MCP servers) it gives the AI those
  facts, so the AI can interview you briefly and write your portfolio, resume or tech stack career —
  only from GitFolio's numbers and your answers.
- **You decide what leaves your computer.** GitFolio starts in local mode: nothing goes out on its own,
  and it calls the aline.team API, with daily totals, only when you look up your type or
  portfolio. Syncing with aline.team after every push is a choice you make in `gitfolio init`. What AI
  tools see of each repository is yours to set as well (`gitfolio config ai`).

## Install

GitFolio needs `git`. The install scripts check the SHA-256 checksum before installing. A PDF portfolio
also needs Chrome, Edge or Chromium (optional; Markdown needs nothing).

### macOS

macOS 13 (Ventura) or later.

```sh
brew install Alineteam-Inc/tap/gitfolio
```

or `curl -fsSL https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.sh | sh`

### Linux

```sh
curl -fsSL https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.sh | sh
```

or, with Homebrew, `brew install Alineteam-Inc/tap/gitfolio`

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.ps1 | iex
```

### Claude Code

Install the CLI above first, then in Claude Code:

```sh
/plugin marketplace add Alineteam-Inc/GitFolio-CLI
/plugin install gitfolio@alineteam
```

Then ask: *"Set up GitFolio for this repository."*

### Codex

Install the CLI above first, then:

```sh
codex plugin marketplace add Alineteam-Inc/GitFolio-CLI
codex plugin add gitfolio@alineteam
```

Then ask: *"Use GitFolio to set up GitFolio for this repository."*

### Other AI tools

Any tool that runs MCP servers can use GitFolio: add the command `gitfolio mcp`. What the plugin adds
(tools and skills) is in [plugins/gitfolio](plugins/gitfolio).

### Update

GitFolio checks for a new release once a day when you run it in a terminal and asks before updating.
To update right away, run `gitfolio update`.

## Get started

```sh
gitfolio init
```

`gitfolio init` shows the data policy and asks you to agree, signs you up or logs you in with a code
sent to your email, and lets you choose the repositories. It adds git hooks to the chosen repositories,
on this computer only. It then asks what matters before anything happens: what AI coding tools may see
of the repositories, and whether to sync with aline.team automatically (no means local mode). Your profile on the web is at
[aline.team/profile](https://aline.team/profile). GitFolio needs git; when it is missing, init offers to
install it with your system's installer (the Command Line Tools on macOS, winget on Windows, the
package manager on Linux).

With an AI tool, the plugin's `setup` skill does the same in conversation: it shows you the policy,
asks for your email and the code, and asks the same questions.

## Use it

```sh
gitfolio portfolio               # your portfolio as Markdown to edit, or as a PDF (it asks which)
gitfolio stats                   # your developer type and five scores
gitfolio stack                   # how long you used each language, framework and library
gitfolio status                  # what GitFolio collected, per repository
```

Or ask your AI tool, for example:

- *"Write my career description for this job posting"* (paste it) — it asks a few questions per
  project, then writes from GitFolio's numbers and your answers.
- *"Write my tech stack career for my resume."*
- *"Explain my developer type and how to read my scores."*
- *"Why are my recent commits missing on aline.team?"*

## What leaves your computer

- **Only after you log in, only from repositories you chose, only your own pushed commits.**
- **Local mode (the default):** daily statistics of each repository only — counts per day of your
  commits, changed files per language, files you created and later changes to them (by anyone; other
  people's commits are only counted here, never sent), substantive commits, files that show a tool
  (`Dockerfile`, `*.tf`, ...) and the languages used. No hashes, messages, paths or branches. GitFolio
  calls the aline.team API with them when you look up your type or portfolio (or run
  `gitfolio sync`). While aline.team does not take them yet, the commit records go instead, as before
  local mode (`--dry-run` may still show the statistics).
- **Automatic sync, if you turn it on:** also, for each of your commits, the repository (git service and
  `owner/repo`), commit hash, branch, time, message (masked on your computer), whether AI agents helped
  and which, and for each file its path in the repository, lines added/deleted and whether the commit
  created it; for files you created, when someone else first changed them (path and time only). All
  under one email, your primary work email (`gitfolio email`).
- **Only under emails you verified:** aline.team takes your commits under an email verified with a code
  sent to it (`gitfolio init`, `gitfolio email verify`), or a GitHub or GitLab noreply address.
- **Your account and this computer:** your email and the work emails you verify; at login, the OS and
  CPU type (e.g. `macOS arm64`), screen language and time zone; with every request, the GitFolio
  version, OS and CPU type.
- **AI coding tools** (`gitfolio mcp`) see, per repository, what you allow (`gitfolio config ai`): its
  real name (default), anonymous or another name; commit messages and branch names only if you turn
  them on; or nothing. File paths, folder names, hashes and emails are never shown. What the tools
  return becomes part of the conversation, so your AI tool's provider receives it.
- **Never collected:** source code or file contents, where repositories are on your computer, the
  remote address and credentials, other people's commits and emails.
- **Package manager files** (`package.json`, …) are read only if you approve each file, and only when
  you ask (`gitfolio deps scan`, or `gitfolio deps auto on`). Pushes never read them. Only the names of
  the dependencies in modules your commits changed are sent (no versions or file paths), and aline.team
  adds the frameworks and libraries they show to your tech stack. `gitfolio deps off` removes them there
  too.
- **Check it yourself:** `gitfolio sync --dry-run` shows what would be sent, `gitfolio history` shows
  every git command GitFolio ran (recorded by git itself), and
  `gh attestation verify <archive> -R Alineteam-Inc/GitFolio-CLI` proves a release was built from this source.
- HTTPS only, to Alineteam Inc. (United States). [Terms](https://aline.team/terms) ·
  [Privacy Policy](https://aline.team/privacy)

## Commands

```sh
gitfolio init                    # setup: data policy, sign-up/login, repositories, hooks, AI tools, sync mode
gitfolio login | whoami | logout # log in with an email code / show the account / log out
gitfolio add | remove <path>     # register a repository / unregister (--purge: delete its data here and on aline.team, web link included)
gitfolio list                    # registered repositories, hooks and dependency files
gitfolio status [name]           # per repository: commits collected and sent, file changes, lines, AI use, languages, dependencies
gitfolio stats [--refresh]       # your developer type and five scores from aline.team (saved here after the first time;
                                 # in local mode it calls the aline.team API each time)
gitfolio stack                   # how long you used each language, framework and library (worked out here)
gitfolio portfolio [file.md|file.pdf]
                                 # your portfolio from your git history: developer type, tech stack, repositories
                                 # (Markdown to edit or a PDF printed with Chrome, Edge or Chromium;
                                 # no file named: it asks which; --from file.md prints your edited one)
gitfolio mcp                     # serve GitFolio to AI coding tools (Claude Code, Codex) over stdio
gitfolio <command> --json        # for programs: one JSON document on stdout (status, sync, stats, stack, login,
                                 # email, add, deps, config, schedule, uninstall, version …; `gitfolio help`)
gitfolio sync [--dry-run]        # send what aline.team does not have yet (--dry-run: just show it)
gitfolio config autosync on|off  # on: sync with aline.team after every push; off (default): local mode
gitfolio config ai               # what AI coding tools see of each repository (name, messages, branches)
gitfolio config mask add <word>  # hide a customer or project name in commit messages
gitfolio email add <email>       # another work email of yours (verify it with the emailed code to merge
                                 # repositories you also linked on aline.team)
gitfolio schedule 09:00          # optional daily sync with automatic sync (off: schedule off)
gitfolio deps [on|off|scan]      # dependency detection: per-file approval, read on request
gitfolio history                 # git commands GitFolio ran
gitfolio update                  # install the newest release now
gitfolio uninstall               # before removing GitFolio: unregister all, put back earlier hooks, log out
                                 # (--purge-local: also delete the data here; nothing on aline.team is deleted)
gitfolio help                    # everything else
```

A mistyped command, word or flag gets the ones you may have meant (`gitfolio sync --dryrun` →
`gitfolio sync --dry-run`).

## License and name

The code is under the [Apache License 2.0](LICENSE): use it, change it, fork it. A fork or other
derived work must keep [LICENSE](LICENSE) and [NOTICE](NOTICE), which name Alineteam Inc. as the
original author, and must mark the files it changed. The **name "GitFolio", "aline.team", their logos
and the developer type icons (`gitfolio stats`) are not part of the license** — a modified or forked
version must use its own name, logo and icons and must not suggest that Alineteam made or endorses it.
Using the aline.team API is subject to the [aline.team Terms of Service](https://aline.team/terms).
Releases up to v0.4.1 were published under MIT.

© 2026 Alineteam Inc.

---

## 한국어

**내 git 이력으로 만드는 개발 포트폴리오 — 코드는 한 줄도 내보내지 않습니다.**

### GitFolio는

개발자의 가장 좋은 작업은 대개 밖에서 볼 수 없는 비공개·회사 저장소에 있습니다. GitFolio는 그 이력을 쓸 수 있는
사실로 바꿉니다. 무엇을, 얼마나 오래, 어떤 언어·프레임워크로, 어떤 성격의 작업을 했고, 그중 얼마를 AI와 함께 했는지입니다.

- **명령줄 도구**(macOS·Linux·Windows, [Alineteam Inc.](https://aline.team))입니다. 내가 작성하고 push한 커밋의
  메타데이터를 이 컴퓨터에서 읽습니다. 소스 코드는 읽지 않습니다.
- **[aline.team](https://aline.team) API로** 개발자 유형(능력치 다섯 가지, AI 포함·미포함)과 기술별 사용 기간을 계산합니다.
- **AI 코딩 도구와 함께**(Claude Code, Codex 등 MCP를 쓰는 도구) 이 사실을 AI에게 넘겨, AI가 짧게 묻고 포트폴리오·이력서·
  기술 스택 커리어를 쓰게 합니다. GitFolio의 숫자와 내 답만으로 씁니다.
- **무엇이 컴퓨터 밖으로 나갈지는 내가 정합니다.** GitFolio는 로컬 모드로 시작해, 평소에는 아무것도 보내지 않고
  유형·포트폴리오를 볼 때만 일 단위 총계로 aline.team API를 호출합니다. push할 때마다 aline.team과 동기화할지는
  `gitfolio init`에서 고릅니다. AI 도구에 저장소를 어디까지 보일지도 내가 정합니다(`gitfolio config ai`).

### 설치

`git`이 필요합니다. 설치 스크립트는 SHA-256을 검증한 뒤 설치합니다. PDF 포트폴리오에는 Chrome·Edge·Chromium 중 하나가
필요합니다(선택, Markdown은 필요 없음).

**macOS**

macOS 13(Ventura) 이상.

```sh
brew install Alineteam-Inc/tap/gitfolio
```

또는 `curl -fsSL https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.sh | sh`

**Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.sh | sh
```

또는 Homebrew가 있으면 `brew install Alineteam-Inc/tap/gitfolio`

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Alineteam-Inc/GitFolio-CLI/main/install.ps1 | iex
```

**Claude Code** — 위의 CLI를 먼저 설치한 뒤 Claude Code에서:

```sh
/plugin marketplace add Alineteam-Inc/GitFolio-CLI
/plugin install gitfolio@alineteam
```

그다음 *"이 저장소에 GitFolio 설정해 줘"* 라고 요청합니다.

**Codex** — 위의 CLI를 먼저 설치한 뒤:

```sh
codex plugin marketplace add Alineteam-Inc/GitFolio-CLI
codex plugin add gitfolio@alineteam
```

그다음 *"GitFolio로 이 저장소에 GitFolio 설정해 줘"* 라고 요청합니다.

**그 밖의 AI 도구** — MCP 서버를 쓰는 도구라면 `gitfolio mcp` 명령을 추가하면 됩니다. 플러그인이 더하는
도구와 스킬은 [plugins/gitfolio](plugins/gitfolio)에 있습니다.

**업데이트:** 터미널에서 실행하면 하루 한 번 새 버전을 확인하고, 업데이트할지 묻습니다. 바로 하려면
`gitfolio update`를 실행하세요.

### 시작하기

```sh
gitfolio init
```

`gitfolio init`은 데이터 정책을 보여 주고 동의를 받은 뒤, 이메일로 받은 코드로 가입·로그인하고 저장소를 고르게 합니다.
고른 저장소에는 git 훅을 이 컴퓨터에만 추가합니다. 그다음 실제로 무언가 하기 전에 중요한 것을 묻습니다. AI 코딩 도구에 저장소를
어디까지 보일지, aline.team과 자동으로 동기화할지(아니요 = 로컬 모드)입니다. 웹 프로필은
[aline.team/profile](https://aline.team/profile)에서 봅니다. GitFolio는 git이 필요하며, git이 없으면 init이 시스템 설치
도구(macOS는 Command Line Tools, Windows는 winget, Linux는 패키지 관리자)로 설치할지 묻습니다.

AI 도구에서는 플러그인의 `setup` 스킬이 대화로 같은 과정을 진행합니다. 정책을 보여 주고, 이메일과 코드를 묻고,
같은 질문을 합니다.

### 쓰기

```sh
gitfolio portfolio               # 고쳐 쓸 Markdown 또는 PDF 포트폴리오 (형식을 물음)
gitfolio stats                   # 개발자 유형과 능력치 다섯 가지
gitfolio stack                   # 언어·프레임워크·라이브러리별 사용 기간
gitfolio status                  # 저장소별로 모은 내용
```

또는 AI 도구에 이렇게 요청합니다.

- *"이 채용 공고에 맞춰 경력기술서 써 줘"*(공고 붙여넣기) — 프로젝트마다 몇 가지를 물은 뒤 GitFolio의 숫자와 내 답으로 씁니다.
- *"이력서에 넣을 기술 스택 커리어 써 줘"*
- *"내 개발자 유형과 점수를 설명해 줘"*
- *"최근 커밋이 aline.team에 안 보이는 이유가 뭐야?"*

### 컴퓨터 밖으로 나가는 것

- **로그인한 뒤, 내가 고른 저장소에서, 내가 push한 내 커밋만** 다룹니다.
- **로컬 모드(기본):** 저장소별 일 단위 통계만 씁니다. 날짜별 내 커밋 수, 언어별 변경 파일 수, 내가 만든 파일과 그 이후
  변경 수(누가 고쳤든. 다른 사람의 커밋은 여기서 세기만 하고 보내지 않음), 실질 커밋 수, 도구를 알 수 있는 파일
  (`Dockerfile`, `*.tf` 등), 사용한 언어입니다. 해시·메시지·경로·브랜치는 없습니다. 유형·포트폴리오를 볼 때(또는
  `gitfolio sync`를 실행할 때) 이것으로 aline.team API를 호출합니다. aline.team이 아직 이 통계를 받지 않으면 로컬 모드
  이전처럼 커밋 기록을 대신 보냅니다(`--dry-run`에는 통계로 보일 수 있음).
- **자동 동기화를 켜면:** 커밋마다 저장소(git 서비스와 `소유자/저장소`), 커밋 해시·브랜치·시각, 커밋 메시지(이 컴퓨터에서
  마스킹), AI 에이전트 사용 여부와 종류, 파일마다 저장소 안 경로·추가·삭제 줄 수·이 커밋에서 새로 만든 파일인지,
  내가 만든 파일을 다른 사람이 처음 고친 시각(경로와 시각만)도 보냅니다. 모두 대표 작업 이메일(`gitfolio email`) 하나로 보냅니다.
- **인증한 이메일로만:** aline.team은 메일로 받은 코드로 인증한 이메일(`gitfolio init`, `gitfolio email verify`)이나
  GitHub·GitLab noreply 주소로 쓴 커밋만 받습니다.
- **계정과 이 컴퓨터:** 계정 이메일과 인증한 작업 이메일, 로그인할 때 OS·CPU 종류(예: `macOS arm64`)·화면 언어·시간대,
  요청마다 GitFolio 버전·OS·CPU 종류
- **AI 코딩 도구**(`gitfolio mcp`)는 저장소마다 내가 허용한 만큼만 봅니다(`gitfolio config ai`). 저장소 실제 이름(기본)·익명·
  별칭, 켜면 커밋 메시지와 브랜치 이름, 또는 아무것도 보지 않게 할 수 있습니다. 파일 경로·폴더 이름·해시·이메일은 보이지
  않습니다. 도구가 돌려준 내용은 대화의 일부가 되어 AI 도구 제공사에 전달됩니다.
- **수집하지 않는 것:** 소스 코드·파일 내용, 내 컴퓨터의 저장소 위치, 원격 저장소 주소와 인증 정보, 다른 사람의 커밋과 이메일
- **패키지 매니저 파일**(`package.json` 등)은 파일별로 승인한 것만, 요청할 때만 읽습니다(`gitfolio deps scan` 또는
  `gitfolio deps auto on`). push 때는 읽지 않습니다. 내 커밋이 바꾼 모듈의 의존성 이름만 보내고(버전·파일 경로 제외),
  aline.team이 그 프레임워크·라이브러리를 기술 스택에 넣습니다. `gitfolio deps off`로 끄면 aline.team에서도 지웁니다.
- **직접 확인:** `gitfolio sync --dry-run`으로 보낼 내용을, `gitfolio history`로 실행한 git 명령(git이 직접 기록)을 봅니다.
  `gh attestation verify <파일> -R Alineteam-Inc/GitFolio-CLI`로 릴리스가 이 소스로 빌드됐음을 확인합니다.
- HTTPS로만 Alineteam Inc.(미국)에 보냅니다. [이용약관](https://aline.team/terms) ·
  [개인정보처리방침](https://aline.team/privacy)

### 명령

```sh
gitfolio init                    # 설정: 데이터 정책, 가입·로그인, 저장소, 훅, AI 도구, 동기화 방식
gitfolio login | whoami | logout # 이메일 코드로 로그인 / 계정 확인 / 로그아웃
gitfolio add | remove <경로>     # 저장소 등록 / 해제 (--purge: 이 컴퓨터와 aline.team의 데이터·웹 연동 삭제)
gitfolio list                    # 등록 저장소, 훅, 의존성 파일
gitfolio status [이름]           # 저장소별 모은 커밋·보낸 수, 파일 변경, 줄 수, AI 사용, 언어, 의존성
gitfolio stats [--refresh]       # aline.team의 개발자 유형과 능력치 다섯 가지 (처음 받은 뒤 이 컴퓨터에 저장.
                                 # 로컬 모드에서는 매번 aline.team API를 호출)
gitfolio stack                   # 언어·프레임워크·라이브러리별 사용 기간 (이 컴퓨터에서 계산)
gitfolio portfolio [파일.md|파일.pdf]
                                 # git 이력으로 만든 포트폴리오: 개발자 유형, 기술 스택, 저장소
                                 # (고쳐 쓸 Markdown 또는 Chrome·Edge·Chromium으로 인쇄한 PDF.
                                 # 파일 이름이 없으면 형식을 물음, --from 파일.md는 고친 파일을 PDF로)
gitfolio mcp                     # AI 코딩 도구(Claude Code, Codex)에 GitFolio를 MCP 서버로 제공
gitfolio <명령> --json           # 프로그램용: 표준 출력에 JSON 하나 (status, sync, stats, stack, login, email,
                                 # add, deps, config, schedule, uninstall, version 등. `gitfolio help` 참고)
gitfolio sync [--dry-run]        # aline.team에 없는 것만 전송 (--dry-run: 보여 주기만)
gitfolio config autosync on|off  # on: push할 때마다 aline.team과 동기화, off(기본): 로컬 모드
gitfolio config ai               # AI 코딩 도구가 저장소별로 볼 수 있는 범위 (이름, 메시지, 브랜치)
gitfolio config mask add <단어>  # 커밋 메시지 속 고객사·프로젝트명 가리기
gitfolio email add <이메일>      # 다른 작업 이메일 추가 (메일로 온 코드로 인증하면 aline.team 웹에서도
                                 # 연동한 저장소와 하나로 합쳐짐)
gitfolio schedule 09:00          # 자동 동기화일 때 매일 동기화 (선택, 해제: schedule off)
gitfolio deps [on|off|scan]      # 의존성 분석: 파일별 승인, 요청할 때만 읽기
gitfolio history                 # GitFolio가 실행한 git 명령
gitfolio update                  # 최신 버전을 바로 설치
gitfolio uninstall               # GitFolio를 지우기 전에: 등록 해제, 원래 훅 복원, 로그아웃
                                 # (--purge-local: 이 컴퓨터의 데이터도 삭제. aline.team의 데이터는 그대로)
gitfolio help                    # 그 밖의 명령
```

명령·단어·플래그를 잘못 입력하면 비슷한 것을 안내합니다(`gitfolio sync --dryrun` → `gitfolio sync --dry-run`).

### 라이선스와 이름

코드는 [Apache License 2.0](LICENSE)입니다. 자유롭게 쓰고, 고치고, 포크할 수 있습니다. 포크하거나 고쳐 만든 것은
원작자 Alineteam Inc.를 밝힌 [LICENSE](LICENSE)와 [NOTICE](NOTICE)를 함께 배포하고, 고친 파일에 고쳤다고 표시해야
합니다. **"GitFolio"·"aline.team" 이름과 로고, 개발자 유형 아이콘(`gitfolio stats`)은 라이선스에 포함되지
않습니다.** 고치거나 포크한 버전은 다른 이름·로고·아이콘을 써야 하고, Alineteam이 만들었거나 보증한 것처럼 보여서는
안 됩니다. aline.team API 사용은 [aline.team 이용약관](https://aline.team/terms)을 따릅니다. v0.4.1까지의 릴리스는
MIT로 배포했습니다.

© 2026 Alineteam Inc.
