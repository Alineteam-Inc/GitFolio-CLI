# GitFolio — 작업 안내 (Claude Code)

이 파일은 어느 기기에서든 이 저장소를 열면 먼저 읽힌다. 지금까지의 결정과 진행은 아래 문서에 있고, 이 파일은 그 입구다.

## 먼저 할 것

- **사용자에게는 항상 한국어로 답한다.** 상태 보고, 짧은 확인, 명령 결과 직후 답변도 한국어. 코드·식별자·커밋 메시지·CLI 출력 문구 원문은 영어 가능
- 이어서 작업할 때 읽는 순서
  1. 비공개 공유 문서 — 잔여 작업, 로드맵, 서버 계약, 서버 상태와 결정 경위. 어디 있는지는 이 파일에 적지 않는다: 로컬 메모리에 있으면 그것을, 없으면 사용자에게 묻는다
     - 설계와 결정 기록(날짜 포함, 옛 `docs/DESIGN.md`)도 비공개 문서에 있다. 설계가 바뀌면 그것을 먼저 고친다
     - 로컬에 `docs/API.md`·`docs/REMAINING.md`·`docs/ROADMAP.md`·`docs/DESIGN.md`가 남아 있어도 옛 사본이다. git에 올리지 않는다(`.gitignore`, 2026-10-02·03·16 사용자 결정)

## 프로젝트

- GitFolio: 본인이 작성·push한 커밋의 **메타데이터만** 모아(소스 코드 없음) Alineteam Inc.의 aline.team으로 보내 개발 프로필·이력서를 만드는 CLI
- Go, 표준 라이브러리만, `CGO_ENABLED=0`, git은 명령 실행으로 사용. Apache-2.0 + NOTICE (Alineteam Inc., v0.4.1까지 MIT). 바이너리 `cmd/gitfolio`
- 대상 OS: **macOS·Linux·Windows** 모두 (amd64·arm64). 경로는 `filepath`, OS별 코드는 `platform_unix.go`·`platform_windows.go`
- 서버는 별도 비공개 저장소. 계약과 서버 상태는 위 비공개 문서가 기준. CLI 상태가 바뀌면(dev 검증 통과, `main` 병합, 릴리스) 그 문서에 기록한다. 문서를 볼 수 없으면 사용자에게 확인한다
- **이 저장소는 공개다.** 서버 내부 정보(서버 주소·기술 스택·키 형식·제한 수치·약점, DB·검색 인덱스 이름, 배포 경위, 서버 커밋), 개인 이메일, 비공개 문서의 위치·구성, 작업 계획·진행 현황·잔여 작업은 커밋하지 않고 비공개 문서에 쓴다 (2026-10-02·03 사용자 결정)

## 브랜치와 환경

| 브랜치 | 환경 | 서버 | 역할 |
|---|---|---|---|
| `main` | prod | `https://aline.team/api` (CLI 기본값) | 확정 사양. 릴리스 태그(`v*`)와 설치 스크립트(`install.sh`·`install.ps1`) |
| `stage` | dev | dev 서버, 주소는 비공개 문서 (`gitfolio config api-url` — 소스 빌드에서만, 릴리스 바이너리는 항상 운영) | 서버 새 계약 테스트. 2026-10-01 `main`에 병합 |

- 흐름: 기능 브랜치 → `stage`(세 OS CI + dev 실검증) → `main`(릴리스). 별도 `prod` 브랜치는 만들지 않는다
- CI(`.github/workflows/ci.yml`): `main`·`stage` push와 PR마다 ubuntu·macos·windows에서 vet·test·설치 스크립트. 릴리스는 세 OS 통과 후. 같은 커밋이 이미 통과했으면(stage → main, 릴리스 태그) 다시 돌리지 않고, `.md`·LICENSE·NOTICE만 바뀐 push는 건너뛴다. 다시 돌리려면 `gh workflow run ci.yml --ref <브랜치>`

## 작업 규칙

- 커밋은 **기능별로 나눠** 만들고 push한다. 메시지는 영어
- 릴리스 버전은 Semantic Versioning(`MAJOR.MINOR.PATCH`, 지금은 `0.y.z`): 새 기능 = MINOR, 버그 수정 = PATCH. 정식 1.0 전에는 `0.y.z`
- 확인: `gofmt -l ./cmd`, `for os in darwin linux windows; do GOOS=$os go vet ./...; done`, `go test ./...`
- 화면 문구는 `cmd/gitfolio/i18n.go`에 en·ko·ja를 함께 넣는다 (`TestMessagesComplete`, `TestMessageVerbsMatch`). 출력 모양: 터미널에서는 Inquirer.js 형식(질문 `?`, 결과 `✔`, 경고 `⚠`, 오류 `✖`, 안내 `ℹ`, 색), 그 밖에서는 `GitFolio :`·`? `. 문단 사이 빈 줄 하나 (설계 기록 7.1)
- **소스 빌드(`go install`, 버전 `dev`)는 데이터 폴더가 따로다** (`gitfolio-dev`, 릴리스는 `gitfolio`). 원래 Mac의 실제 사용은 brew 릴리스(운영), 소스 빌드는 `~/go/bin/gitfolio-dev`로 설치해 dev 서버 테스트에 쓴다
- **미병합 브랜치 빌드는 테스트용 HOME에서만 실행한다** (`HOME=<임시 폴더> GIT_CONFIG_GLOBAL=<임시 파일>`). `sync --dry-run` 같은 명령도 먼저 scan하므로 로컬 데이터를 새 형식으로 바꿀 수 있다
- 보안: 로그인 토큰은 화면·로그·커밋에 절대 출력하지 않는다. 다른 서비스 인증 정보(git credential, `gh`)는 읽지도 보내지도 않는다. HTTPS만
- 실기 확인: Linux는 colima Docker의 Ubuntu 컨테이너(systemd 컨테이너 포함), Windows는 GitHub Actions `windows-latest`

## 주요 결정 (상세는 비공개 문서)

- 수집: 작성자가 본인인 커밋의 메타데이터. 마스킹은 **커밋 메시지만**. 브랜치 이름·저장소 안 파일 경로·저장소 이름은 원문
- aline.team 로그인 필수(로그인 전 수집 없음). 기기 키·별도 동의 단계 없음(가입 = 약관·개인정보처리방침 동의)
- 본인 판별: 작업 이메일(`gitfolio email`, 0.2.0부터 코드 인증) + 저장소별 git 이메일. 전송은 대표 이메일 하나
- 로그인이 없거나 만료되면 터미널에서는 바로 로그인으로 이어진다(등록 이메일로 코드, 없으면 이메일 다시 입력). 로그인은 발급한 서버에만 쓴다
- 화면 언어: `GITFOLIO_LANG` → 기기 언어(macOS·Windows) → `LANG` 등. 원래 기기(Mac)는 영어 UI라 `GITFOLIO_LANG=ko`로 쓴다
