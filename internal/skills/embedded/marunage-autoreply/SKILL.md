---
name: marunage-autoreply
description: 権限境界を明示しながら、既知依頼への返信を自動化するスキル
---
<!-- version: 0.1.0 -->
# marunage-autoreply

Automatically responds to incoming requests while respecting explicit permission
boundaries. Permission categories are read from `~/.marunage/autoreply.toml`.

This file may be customised. Run `marunage setup --skills --check-updates` to
compare your on-disk copy against the embedded version.

## Permissions

Read from `~/.marunage/autoreply.toml`. If the file does not exist, the
built-in defaults apply:

- Allow: `schedule_adjustment`, `information_sharing`, `known_questions`
- Deny:  `personal_information`, `contracts`, `financial_decisions`, `personnel_matters`

## Auto-Reply OK

Auto-reply is permitted for these categories:

- **known_questions** — factual questions with deterministic answers
- **schedule_adjustment** — meeting-time change requests, availability confirmations
- **information_sharing** — confirmations of "please forward this to the team" style requests

When a message matches an OK category, compose and send the reply immediately
(unless draft mode is active — see the Draft Mode section below).

## Auto-Reply NG (NEVER auto-reply)

> ⚠️ The following categories MUST NEVER be auto-replied, regardless of configuration.

- **personal_information** — requests for personal data, addresses, IDs, or credentials
- **contracts** — contract acceptance, amendment, or any legal commitment
- **financial_decisions** — expense approval, invoices, payment authorisation
- **personnel_matters** — hiring, firing, performance reviews, disciplinary actions

If a message matches any NG category, stop immediately and escalate to the human
operator. Do NOT compose a reply. Log the escalation reason to
`~/.marunage/logs/audit.log` in JSONL format with fields:
`{action:"autoreply.escalation", category, task_id, ts}`.
(The audit-log write is performed by the Executor layer; see Draft Mode note.)

## Draft Mode

When `draft_mode.enabled = true` in `~/.marunage/autoreply.toml`, the skill
composes the reply but does **not** send it. The draft is saved to
`~/.marunage/autoreply-drafts/<task-id>.md` for human review before sending.

The `--draft-only` CLI flag overrides the config file for a single run.
(Note: draft file I/O and `--draft-only` CLI are implemented in the Executor
layer in a follow-up phase; the boundary config flag `draft_mode.enabled` is
available now.)

This skill does not create a new bi-directional chat channel. It sends one
reply per task through the existing Source adapter and does not receive
follow-up messages automatically.

## Non-Goals

- **No new bi-directional chat channel**: This skill sends one reply per task
  through the existing Source adapter. It does not receive follow-up messages
  automatically or establish an ongoing conversation loop.
- **No category discovery**: This skill does not classify incoming messages into
  categories; that is the responsibility of the caller (triage / executor layer).
- **No credential storage**: The autoreply.toml file stores only category lists
  and mode flags — never credentials, tokens, or secrets.

## Output Format

On successful auto-reply:

```
## Result
Replied to <source>/<id>: <one-line summary>

## Draft Path
<path>  (only when draft mode is active; omit when actually sent)
```

On escalation (NG category detected):

```
## Escalation
category: <category>
reason: <one sentence>
action: human review required
```
