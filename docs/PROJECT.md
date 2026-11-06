# Project record

## Objective

Provide a focused Go client for the Jira Cloud REST v3 API that covers enhanced JQL search, issue transitions and issue properties. The client handles throttling, retries of idempotent calls, and path escaping, and it can be tested offline through an injectable HTTP Doer.

## Scope

- In scope: the public API documented in [README.md](../README.md): JQL search with token pagination, listing and performing transitions (by id or by name), and reading and writing issue properties.
- In scope: input validation before any request, bounded retries for 429 and 503, redirect refusal by default, body size limits, and redacted credentials.
- Out of scope: OAuth 2.0 (3LO), creating or editing issues, comments, Atlassian Document Format (ADF), agile APIs and webhook APIs.
- Out of scope: any dependency outside the standard library, and any I/O without an accepted ADR.

## Timeline

| Date | Event |
|---|---|
| 2026 | Repository created from the factory utility template. |

## Effort metrics

Values are filled by the factory after measurement.

| Metric | Value |
|---|---|
| Tokens (input / output) | 528102 / 104982 |
| Hours (human) | pending measurement |
| Cost (USD) | 0.7291 |

## Factory run u062 (measured from API usage)

| Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|
| generation | claude-sonnet-5-5 | yes | 2585 | 86058 | 0 | 4285 | 0.4333 |
| fix-1 | claude-sonnet-5-5 | no | 8 | 859 | 12386 | 68190 | 0.0464 |
| fix-2 | claude-sonnet-5-5 | no | 8 | 750 | 11618 | 67395 | 0.0433 |
| fix-3 | claude-sonnet-5-5 | no | 8 | 1112 | 11363 | 66554 | 0.0462 |
| fix-4 | claude-sonnet-5-5 | no | 10 | 884 | 9158 | 79340 | 0.0397 |
| fix-1 | claude-sonnet-5-5 | no | 8 | 943 | 7595 | 56160 | 0.0340 |
| fix-2 | claude-sonnet-5-5 | no | 8 | 791 | 5070 | 59084 | 0.0265 |
| fix-3 | claude-sonnet-5-5 | no | 8 | 754 | 4612 | 58643 | 0.0250 |
| fix-4 | claude-sonnet-5-5 | no | 8 | 938 | 5102 | 58622 | 0.0280 |
| docs | claude-haiku-5-5 | no | 7178 | 11893 | 0 | 0 | 0.0067 |

Total: $0.7291. Prices: Anthropic list prices.
