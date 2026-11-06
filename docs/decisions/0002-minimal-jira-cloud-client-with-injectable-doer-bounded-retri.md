# Minimal Jira Cloud client with injectable Doer, bounded retries and no redirects

Status: accepted (generated, pending human review)

## Context

Automation that only searches issues, moves them through a workflow and stores metadata does not need a large Jira client built around the deprecated search endpoint. Such scripts also tend to mishandle 429 throttling, retry non-idempotent calls, escape paths badly and leak credentials through redirects or logs. The library must be testable offline and use only the standard library.

## Decision

Provide a single package with six operations on the Jira Cloud REST v3 API (enhanced JQL search, list/do transitions, get/set issue properties). All HTTP goes through a small Doer interface and all waiting goes through an injectable Sleep function, so tests use a fake Doer and never wait. The default Doer is an http.Client with a 30s timeout and CheckRedirect returning http.ErrUseLastResponse, so Basic credentials never follow a redirect. 429 is always retried because the request was rejected; 503 is retried only for GET, PUT and search, so a transition is never sent twice. Retry-After is honoured only as delay-seconds (RFC 9110 section 10.2.3), and the delay is capped at 60s with 1s, 2s, 4s fallback. Inputs are validated before any request (https base URL, issue key and property key syntax, 32768-byte property values, JQL, field and limit bounds). Response bodies are limited to 10 MiB. All errors are *Error sentinels with a stable Code, or *APIError with messages truncated to 200 runes. BasicAuth and Client redact the token in every formatting verb, and OnEvent only receives method, path template, status and attempt.

## Consequences

The client is small, fully offline-testable and safe by default, but it intentionally covers little of the Jira API: no OAuth 2.0 (3LO), issue creation or editing, comments, ADF, agile or webhooks. A custom Doer must provide its own no-redirect guarantee. Transport errors are not retried, trading some resilience for never repeating a possibly applied non-idempotent call. Setting MaxRetries to zero means the default of 3, so disabling retries requires a negative value.
