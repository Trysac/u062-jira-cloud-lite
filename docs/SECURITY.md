# Security notes

## Threat model

`github.com/Trysac/u062-jira-cloud-lite` is a library that performs HTTP requests to a Jira Cloud site through a Doer that the caller can replace. The model below separates what the code defends against from what it does not.

### Defended

- **Credential leakage through redirects**: the default HTTP client returns the redirect response (`http.ErrUseLastResponse`), so the Authorization header is never sent to a redirect target. A 302 is returned as `*APIError`.
- **Credential leakage through formatting**: `BasicAuth.String` and `BasicAuth.GoString` print `***` for the token, and the client's own formatting does not include it.
- **Cleartext transport**: the base URL must be https. Plain http is accepted only for 127.0.0.1, ::1 and localhost.
- **Path injection**: issue keys and numeric ids are validated against fixed patterns, and property keys are path-escaped before being placed in the URL.
- **Unbounded responses**: bodies are read through a limit of 10 MiB plus one byte, and larger bodies return `ErrResponseTooLarge`.
- **Unbounded retries and waits**: retries are limited by MaxRetries (at most 10), and each delay is capped at 60s. Retries are restricted to 429 and to 503 for idempotent requests, so a transition is not repeated after a 503.
- **Hanging requests**: the default client has a 30s timeout, and cancellation of the context stops retries immediately.
- **Sensitive data in events**: `OnEvent` receives only the method, the path template, the status and the attempt. It never receives JQL text or bodies.
- **Unsafe code**: the library does not use `unsafe` or `os/exec`.

### Not defended

- **Caller-supplied Doer**: a custom Doer receives the full request, including the Authorization header, and is responsible for its own redirect and logging behaviour.
- **Token storage**: the library does not store or protect the API token. The caller must keep it out of source control and logs.
- **Authorization**: the library does not check permissions. Jira decides whether an operation is allowed.
- **JQL content**: JQL text is passed to Jira unchanged. The library does not filter or sanitize it.
- **Server certificate policy**: the library uses the system TLS defaults and does not pin certificates.
- **Rate limits beyond retries**: when retries are exhausted, the caller receives `ErrRateLimited`. The library does not queue or schedule requests across calls.
- **Response content**: decoded data is not checked beyond JSON decoding.

## Publishing

Releases are git tags served through the Go module proxy and checksum database. No registry token is stored in the repository.

Supply-chain controls: standard library only, `go.sum` verification, `govulncheck` in the local gates, Dependabot weekly updates, actions pinned by commit SHA, and minimal workflow permissions. `gitleaks` runs locally and in CI, and `.env` files are git-ignored.

To report a suspected vulnerability, contact the maintainer privately and do not open a public issue.
