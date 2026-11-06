# Architecture

## Purpose

`github.com/Trysac/u062-jira-cloud-lite` is a small, dependency-free Go library. It is a minimal Jira Cloud REST v3 client for enhanced JQL search, issue transitions and issue properties, with safe retries and no redirects. It lives in package `jiracloudlite` in the repository root.

## Shape

```text
caller code --> package jiracloudlite (repository root)
                    |
                    +-- Client: builds requests, validates input, retries
                    +-- Doer: injected HTTP client (default: http.Client, 30s timeout, no redirects)
```

- **Public API**: every exported identifier of the root package. Unexported identifiers may change without a major version.
- **Request flow**: inputs are validated before any request is sent. Paths are built from escaped segments, and property keys are set as URL.RawPath so that %2F survives.
- **Retries**: 429 is retried for every request. 503 is retried only for GET, PUT and search. Waits go through `Options.Sleep`, and cancellation of the context stops the loop immediately.
- **Errors**: invalid input returns a sentinel error or a `*Error` value. Non-2xx responses return `*APIError`. Callers branch with `errors.Is`.
- **Credentials**: `BasicAuth` redacts the token in `String` and `GoString`, so formatting never prints it.
- **Side effects**: the only I/O is the HTTP exchange performed through the Doer.

## Quality contract

`scripts/check` is the single definition of the gates; `fabrica.json` tells the factory and CI how to run it. See [TESTING.md](TESTING.md).

## Decisions

Architecture decisions are recorded in [decisions/](decisions/) as numbered ADRs.
