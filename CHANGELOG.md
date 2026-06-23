# Changelog

## [v0.0.15](https://github.com/haruotsu/marunage/compare/v0.0.14...v0.0.15) - 2026-06-23

- PR-R04: data model (plan_* columns) + [manage] config section by @haruotsu in https://github.com/haruotsu/marunage/pull/117
- feat(collect): add collection layer (PR-R02) by @haruotsu in https://github.com/haruotsu/marunage/pull/118
- PR-R03: internal/manage skeleton (verdict registry, rule engine, LLM-free Plan) by @haruotsu in https://github.com/haruotsu/marunage/pull/120
- feat(exec): Executor 抽象を導入し cmux を一実装に降格 (PR-R01) by @haruotsu in https://github.com/haruotsu/marunage/pull/119
- PR-R08: localExecutor (direct child process, Attachable 非対応) by @haruotsu in https://github.com/haruotsu/marunage/pull/121
- PR-R07: tmuxExecutor + backend-agnostic Executor conformance suite by @haruotsu in https://github.com/haruotsu/marunage/pull/122
- PR-R05: wire cmd/marunage into the collect → manage → exec pipeline by @haruotsu in https://github.com/haruotsu/marunage/pull/123
- PR-R06: LLM スコアリング本実装 + marunage-manage skill + コスト制御 by @haruotsu in https://github.com/haruotsu/marunage/pull/124
- docs(readme): document the collect → manage → exec layering by @haruotsu in https://github.com/haruotsu/marunage/pull/125
- feat(exec/herdr): herdr backend (exec 抽象の3つ目の実装) by @haruotsu in https://github.com/haruotsu/marunage/pull/126
- fix(exec): make the local executor usable (backend.New wiring + graceful reaper skip) by @haruotsu in https://github.com/haruotsu/marunage/pull/127
- fix(sources): wire calendar gws client + doctor gws-auth check by @haruotsu in https://github.com/haruotsu/marunage/pull/128
- fix(gmail): fetch with format=full so Candidate.Body is populated by @haruotsu in https://github.com/haruotsu/marunage/pull/129
- feat(web): clickable status cards + Skipped card + pending delete button by @haruotsu in https://github.com/haruotsu/marunage/pull/130
- feat(dispatch): resolve task cwd via ghq with default_cwd fallback by @haruotsu in https://github.com/haruotsu/marunage/pull/131
- fix(github): lift is:open into gh search --state flag (discovery returned nothing) by @haruotsu in https://github.com/haruotsu/marunage/pull/132
- feat(slack): agent-agnostic command adapter for discovery (token path kept) by @haruotsu in https://github.com/haruotsu/marunage/pull/133
- fix(slack): populate Body for single-line messages (avoid needs-human escalation) by @haruotsu in https://github.com/haruotsu/marunage/pull/134
- feat(googletasks): gws-CLI client (Google Tasks discovery now works) by @haruotsu in https://github.com/haruotsu/marunage/pull/135
- feat(notion): wire HTTP client + database_id (Notion discovery now works) by @haruotsu in https://github.com/haruotsu/marunage/pull/136
- fix(manage): escalate on empty title not empty body (title-only items are actionable) by @haruotsu in https://github.com/haruotsu/marunage/pull/137
- feat(slack/reaction): Slack Web API client (reaction trigger now works) by @haruotsu in https://github.com/haruotsu/marunage/pull/138
- feat(doctor): verify Notion token + database_id when notion is enabled by @haruotsu in https://github.com/haruotsu/marunage/pull/139
- feat(cli): implement run-all, open, notify, config edit (drop stubs) by @haruotsu in https://github.com/haruotsu/marunage/pull/141
- fix(config): make `config edit` rollback safe and audited by @haruotsu in https://github.com/haruotsu/marunage/pull/142
- refactor(fsutil): share AtomicWrite + config edit .bak parity with bounded retention by @haruotsu in https://github.com/haruotsu/marunage/pull/143
- fix(web): preserve http.Flusher through the access-log wrapper (SSE was dead in prod) by @haruotsu in https://github.com/haruotsu/marunage/pull/144
- fix(loop): persist empty-title needs-human escalations (was silently dropped) by @haruotsu in https://github.com/haruotsu/marunage/pull/145
- fix(web): fail closed on --remote without auth acknowledgement by @haruotsu in https://github.com/haruotsu/marunage/pull/146
- feat(daemon): add `daemon logs` and fix onboarding docs by @haruotsu in https://github.com/haruotsu/marunage/pull/147
- feat(web): delete tasks directly from the task list (implements #114) by @haruotsu in https://github.com/haruotsu/marunage/pull/148

## [v0.0.14](https://github.com/haruotsu/marunage/compare/v0.0.13...v0.0.14) - 2026-05-11
- fix(cli): config wizardのレイアウト崩れを修正（raw modeでCRLF出力） by @haruotsu in https://github.com/haruotsu/marunage/pull/109

## [v0.0.13](https://github.com/haruotsu/marunage/compare/v0.0.12...v0.0.13) - 2026-05-11
- fix(cli): config wizard で矢印キーを正しく処理する by @haruotsu in https://github.com/haruotsu/marunage/pull/107

## [v0.0.12](https://github.com/haruotsu/marunage/compare/v0.0.11...v0.0.12) - 2026-05-11
- feat(config): interactive wizard for discovery source selection by @haruotsu in https://github.com/haruotsu/marunage/pull/105

## [v0.0.11](https://github.com/haruotsu/marunage/compare/v0.0.10...v0.0.11) - 2026-05-11
- build: add make install target to put binary on PATH by @haruotsu in https://github.com/haruotsu/marunage/pull/89
- fix: integrate reaper into loop and add CWD validation at add time by @haruotsu in https://github.com/haruotsu/marunage/pull/91
- fix: decouple install from build to prevent root-owned build artifacts by @haruotsu in https://github.com/haruotsu/marunage/pull/92
- fix: allow empty cwd when allowed_cwd_prefixes is configured by @haruotsu in https://github.com/haruotsu/marunage/pull/93
- fix: prevent dispatch agent cmux windows from spawning during tests by @haruotsu in https://github.com/haruotsu/marunage/pull/94
- fix: serve route-specific index.html for Next.js directory routes by @haruotsu in https://github.com/haruotsu/marunage/pull/95
- fix: echo X-CSRF-Token response header and cache it in JS by @haruotsu in https://github.com/haruotsu/marunage/pull/96
- fix: return empty array instead of null from /api/skills/installed by @haruotsu in https://github.com/haruotsu/marunage/pull/97
- remove dispatch-agent workspace startup from marunage web by @haruotsu in https://github.com/haruotsu/marunage/pull/98
- fix: show task list on /tasks and remove LiveStream by @haruotsu in https://github.com/haruotsu/marunage/pull/99
- fix: Metrics/Journal not displaying + Delete navigation bug by @haruotsu in https://github.com/haruotsu/marunage/pull/100
- fix: dispatch pending tasks on shorter interval (dispatch_interval) by @haruotsu in https://github.com/haruotsu/marunage/pull/101
- feat(doctor): add slack-mcp check via claude mcp list by @haruotsu in https://github.com/haruotsu/marunage/pull/102
- fix(doctor): parse claude mcp list new format and fix slack name matching by @haruotsu in https://github.com/haruotsu/marunage/pull/104

## [v0.0.10](https://github.com/haruotsu/marunage/compare/v0.0.9...v0.0.10) - 2026-05-08
- fix(web): wire real cmux dispatch + persistent dispatch agent by @haruotsu in https://github.com/haruotsu/marunage/pull/87

## [v0.0.9](https://github.com/haruotsu/marunage/compare/v0.0.8...v0.0.9) - 2026-05-08
- fix(web): serve _next/ static assets and fix CSP for Next.js by @haruotsu in https://github.com/haruotsu/marunage/pull/85

## [v0.0.8](https://github.com/haruotsu/marunage/compare/v0.0.7...v0.0.8) - 2026-05-08
- docs: fix README setup command and add Python 3.11+ prerequisite by @haruotsu in https://github.com/haruotsu/marunage/pull/83

## [v0.0.7](https://github.com/haruotsu/marunage/compare/v0.0.6...v0.0.7) - 2026-05-07
- fix(dispatch): fix 5 bugs that prevented e2e prompt delivery by @haruotsu in https://github.com/haruotsu/marunage/pull/81

## [v0.0.6](https://github.com/haruotsu/marunage/compare/v0.0.5...v0.0.6) - 2026-05-06
- docs: remove Go Reference badge from READMEs by @haruotsu in https://github.com/haruotsu/marunage/pull/76
- feat(web): replace HTML templates with Next.js 15 frontend + JSON APIs by @haruotsu in https://github.com/haruotsu/marunage/pull/78

## [v0.0.5](https://github.com/haruotsu/marunage/compare/v0.0.4...v0.0.5) - 2026-05-06
- fix(config): accept JSON array syntax for string-slice keys in config set by @haruotsu in https://github.com/haruotsu/marunage/pull/73
- feat(gmail): implement GWSClient via gws CLI shell-out by @haruotsu in https://github.com/haruotsu/marunage/pull/75

## [v0.0.4](https://github.com/haruotsu/marunage/compare/v0.0.3...v0.0.4) - 2026-05-06
- chore: enable blank issues for all contributors by @haruotsu in https://github.com/haruotsu/marunage/pull/65
- docs: add Prerequisites table to README (en/ja) by @haruotsu in https://github.com/haruotsu/marunage/pull/67
- docs: add CONTRIBUTING.md with golangci-lint v2.12.1 install instructions by @haruotsu in https://github.com/haruotsu/marunage/pull/69
- docs: add REST API reference for marunage web by @haruotsu in https://github.com/haruotsu/marunage/pull/70
- feat(secrets): implement pass backend by @haruotsu in https://github.com/haruotsu/marunage/pull/71
- refactor(cli): consolidate source plugin registration into registerBuiltin (PR-70) by @haruotsu in https://github.com/haruotsu/marunage/pull/72

## [v0.0.3](https://github.com/haruotsu/marunage/compare/v0.0.2...v0.0.3) - 2026-05-06
- docs: OSS polish — README rewrite, README.ja.md, CODE_OF_CONDUCT, SECURITY, issue templates by @haruotsu in https://github.com/haruotsu/marunage/pull/63

## [v0.0.2](https://github.com/haruotsu/marunage/compare/v0.0.1...v0.0.2) - 2026-05-04

## [v0.0.1](https://github.com/haruotsu/marunage/commits/v0.0.1) - 2026-05-04
- ci: tagpr でリリースを自動化 by @haruotsu in https://github.com/haruotsu/marunage/pull/1
- ci: tagpr に issues: write 権限を追加 by @haruotsu in https://github.com/haruotsu/marunage/pull/2
- ci: actions/checkout を v5 に更新 by @haruotsu in https://github.com/haruotsu/marunage/pull/4
- feat(pr-01): repository bootstrap (go.mod / Makefile / CI / --version) by @haruotsu in https://github.com/haruotsu/marunage/pull/5
- feat: PR-02 CLI スケルトン (cobra) by @haruotsu in https://github.com/haruotsu/marunage/pull/6
- feat(config): PR-03 — config.toml loader with validation, atomic save, and rollback by @haruotsu in https://github.com/haruotsu/marunage/pull/7
- feat(logging): PR-04 ロガー / audit.log 基盤 by @haruotsu in https://github.com/haruotsu/marunage/pull/8
- feat(store): SQLite schema + WAL + embedded migrations (PR-10) by @haruotsu in https://github.com/haruotsu/marunage/pull/9
- feat(store): PR-11 tasks repository layer by @haruotsu in https://github.com/haruotsu/marunage/pull/12
- feat(cli): marunage doctor [--fix] [--json] by @haruotsu in https://github.com/haruotsu/marunage/pull/10
- feat(secrets): keyring abstraction + backend auto-select (PR-30) by @haruotsu in https://github.com/haruotsu/marunage/pull/11
- feat(cmux): PR-40 cmux ワークスペース起動・送信ラッパー by @haruotsu in https://github.com/haruotsu/marunage/pull/13
- feat(store): PR-12 kv_state リポジトリ層 by @haruotsu in https://github.com/haruotsu/marunage/pull/14
- feat(cli): PR-20 add / list / show サブコマンド実装 by @haruotsu in https://github.com/haruotsu/marunage/pull/15
- feat(source/markdown): PR-50 Markdown TODO ソースプラグイン by @haruotsu in https://github.com/haruotsu/marunage/pull/16
- feat(permission): PR-41 権限モード（matcher + waiting_human / failed 遷移） by @haruotsu in https://github.com/haruotsu/marunage/pull/17
- feat(cli): PR-21 done/fail/rm/promote/reopen + 状態遷移バリデーション by @haruotsu in https://github.com/haruotsu/marunage/pull/18
- feat(cli): PR-22 `marunage export` / `marunage clean` by @haruotsu in https://github.com/haruotsu/marunage/pull/19
- feat(dispatch): PR-42 dispatch core (priority + lock_key + max_parallel + ws writeback + prompt build) by @haruotsu in https://github.com/haruotsu/marunage/pull/21
- feat: PR-60 `marunage render` / view.md generator by @haruotsu in https://github.com/haruotsu/marunage/pull/20
- feat(cli): PR-33 marunage init (~/.marunage/ + permission mode prompt) by @haruotsu in https://github.com/haruotsu/marunage/pull/22
- feat(cli): PR-61 marunage status / --watch by @haruotsu in https://github.com/haruotsu/marunage/pull/26
- feat(dispatch): PR-42b dispatch wiring (permission / escalate / redact / UTF-8 / race) by @haruotsu in https://github.com/haruotsu/marunage/pull/25
- feat(completion): PR-43 atomic sentinel completion detection by @haruotsu in https://github.com/haruotsu/marunage/pull/23
- feat(reaper): PR-44 reaper for orphan ws / 24h stuck running by @haruotsu in https://github.com/haruotsu/marunage/pull/24
- feat(secrets): age backend with passphrase-protected vault (PR-31) by @haruotsu in https://github.com/haruotsu/marunage/pull/27
- feat(source): discovery plugin interface, manifest, registry (PR-70) by @haruotsu in https://github.com/haruotsu/marunage/pull/30
- feat(web): web UI foundation with chi/embed/CSRF/SSE (PR-62) by @haruotsu in https://github.com/haruotsu/marunage/pull/29
- feat(cli): marunage setup --skills with embedded SKILLs and diff/force/merge (PR-34) by @haruotsu in https://github.com/haruotsu/marunage/pull/28
- feat(dispatch): PR-72 triage skill integration (judgment_reason wiring) by @haruotsu in https://github.com/haruotsu/marunage/pull/32
- feat(skills): PR-203 shared skill registry (install/list/update) by @haruotsu in https://github.com/haruotsu/marunage/pull/37
- feat(source/gmail): PR-80 Gmail Discovery source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/34
- feat(source/calendar): PR-81 Google Calendar Discovery source by @haruotsu in https://github.com/haruotsu/marunage/pull/35
- feat(source/slack): PR-82 Slack Discovery source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/38
- feat(source/github): PR-83 GitHub Discovery source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/31
- feat(source/googletasks): PR-84 Google Tasks Discovery source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/36
- feat(source/browser): PR-200 Browser adapter source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/33
- feat(source/notion): PR-201 Notion Discovery source plugin by @haruotsu in https://github.com/haruotsu/marunage/pull/41
- feat(loop): PR-71 marunage loop / daemon (discover→dispatch→render) by @haruotsu in https://github.com/haruotsu/marunage/pull/40
- feat(dispatch): PR-102 reflection hook + marunage-reflect skill by @haruotsu in https://github.com/haruotsu/marunage/pull/39
- feat(web): PR-63 Web UI dashboard (running/pending/24h summary/source status) by @haruotsu in https://github.com/haruotsu/marunage/pull/42
- feat(web): task detail page — GET /tasks/{id} by @haruotsu in https://github.com/haruotsu/marunage/pull/44
- feat(web): PR-65 task operation API endpoints and dashboard UI controls by @haruotsu in https://github.com/haruotsu/marunage/pull/43
- feat(slack): introduce slackhog + WebAPIClient with httptest mock by @haruotsu in https://github.com/haruotsu/marunage/pull/45
- feat(review): PR-90 review/promote 強化 — CLI + Web UI + 頻発スキップ検出 by @haruotsu in https://github.com/haruotsu/marunage/pull/46
- feat(source): Slack Reaction Trigger Discovery source (PR-100) by @haruotsu in https://github.com/haruotsu/marunage/pull/47
- feat(project): PR-101 Project Mode — marunage project run <board-url> by @haruotsu in https://github.com/haruotsu/marunage/pull/51
- feat(journal): PR-103 Work Journal - marunage journal start/export by @haruotsu in https://github.com/haruotsu/marunage/pull/50
- feat(autoreply): add marunage-autoreply skill with permission boundary (PR-104) by @haruotsu in https://github.com/haruotsu/marunage/pull/49
- feat(web): PR-105 — metrics, journal, project board endpoints by @haruotsu in https://github.com/haruotsu/marunage/pull/48
- feat(web): Prometheus metrics export endpoint (PR-202) by @haruotsu in https://github.com/haruotsu/marunage/pull/52
- feat(web): live terminal stream for task workspaces (PR-91) by @haruotsu in https://github.com/haruotsu/marunage/pull/53
- feat: PR-42b permission.Matcher dispatcher wiring + prompt injection defense by @haruotsu in https://github.com/haruotsu/marunage/pull/60
- feat: PR-300 daemon full implementation (install/uninstall/restart/logs + singleton) by @haruotsu in https://github.com/haruotsu/marunage/pull/59
- feat: PR-204 Web UI remote publish mode (HTTPS + Bearer auth) by @haruotsu in https://github.com/haruotsu/marunage/pull/58
- feat: PR-303 config wizard / config edit implementation by @haruotsu in https://github.com/haruotsu/marunage/pull/57
- feat: PR-205 marunage stop emergency kill switch by @haruotsu in https://github.com/haruotsu/marunage/pull/56
- feat: PR-301 Discovery IF v2 (adapter_version + update verb) by @haruotsu in https://github.com/haruotsu/marunage/pull/54
- feat: PR-302 HTTP adapter / connector.toml by @haruotsu in https://github.com/haruotsu/marunage/pull/55
