# Security Policy

Thank you for helping keep `marunage` and its users safe.

## Supported Versions

Security fixes are provided on a best-effort basis for:

- `main` (latest development branch)
- The latest released tag

Older releases may not receive fixes. If possible, reproduce issues on the
latest release before reporting.

## Reporting a Vulnerability

Please **do not** open public issues for security reports.

Use one of the following private channels:

1. **GitHub Private Vulnerability Reporting** (preferred):
   [https://github.com/haruotsu/marunage/security/advisories/new](https://github.com/haruotsu/marunage/security/advisories/new)
2. If GitHub private reporting is unavailable, contact the maintainer directly
   and include a link to this repository.

## What to Include

A good report includes:

- A short description of the vulnerability and impact
- Affected version, OS, and runtime context
- Reproduction steps (proof-of-concept is welcome)
- Any relevant logs or stack traces (with secrets redacted)
- Suggested fix or mitigation, if available

## Response Process

Our target response windows are:

- **Acknowledgement:** within 72 hours
- **Triage decision:** within 7 days
- **Fix timeline:** communicated after triage

Severity and complexity may affect timing. We will keep reporters updated.

## Disclosure Policy

- Please allow time for triage and remediation before public disclosure.
- We will credit reporters (unless you prefer to stay anonymous) when a fix is
  published.
- After release, we may publish a security advisory with impact, affected
  versions, and remediation guidance.

## Scope Notes

The highest-priority security concerns for this project include:

- Prompt-injection and unsafe task execution paths
- Secret handling and credential leakage
- Unauthorized Web UI access in remote mode
- Command or path injection in dispatch and plugin execution

Reports outside this scope are still welcome, but triage priority may vary.
