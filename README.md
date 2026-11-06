# jira-cloud-lite

[![Go Reference](https://pkg.go.dev/badge/github.com/Trysac/u062-jira-cloud-lite.svg)](https://pkg.go.dev/github.com/Trysac/u062-jira-cloud-lite)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/Trysac/u062-jira-cloud-lite/actions/workflows/ci.yml/badge.svg)](https://github.com/Trysac/u062-jira-cloud-lite/actions/workflows/ci.yml)

Minimal Jira Cloud REST v3 client in Go for enhanced JQL search, issue transitions and issue properties, with safe retries and no redirects

## Problem

Automation that only searches issues, moves them through a workflow and stores metadata usually ends up importing a large Jira client built around Jira's deprecated search endpoint. Such scripts often handle HTTP 429 throttling, retries of non-idempotent calls and URL path escaping poorly. jira-cloud-lite provides only these operations on the Jira Cloud REST v3 API, with an injectable HTTP client so that all behaviour can be tested offline.

## Install

```sh
go get github.com/Trysac/u062-jira-cloud-lite
```

Requires Go 1.23 or newer. No dependencies outside the standard library.

## Usage

```go
package main
import ("context"; "fmt"; "os"; jiracloudlite "github.com/Trysac/u062-jira-cloud-lite")
func main() {
c, err := jiracloudlite.New("https://example.atlassian.net", jiracloudlite.BasicAuth{Email: "bot@example.invalid", APIToken: os.Getenv("JIRA_API_TOKEN")}, jiracloudlite.Options{})
if err != nil { panic(err) }
issues, err := c.SearchAll(context.Background(), "project = ABC", []string{"summary"}, 50)
fmt.Println(len(issues), err)
}
```

## API summary

- `New(baseURL string, auth BasicAuth, opts Options) (*Client, error)` validates the base URL and returns a client.
- `type BasicAuth struct { Email, APIToken string }` sends `Authorization: Basic base64(email:token)`. `String` and `GoString` print the email and `***`.
- `type Doer interface { Do(*http.Request) (*http.Response, error) }` is the injectable HTTP client.
- `type Options struct { Doer; Sleep func(context.Context, time.Duration) error; MaxRetries int; UserAgent string; OnEvent func(method, pathTemplate string, status, attempt int) }`. `MaxRetries` 0 means the default of 3, the maximum is 10, and a negative value disables retries. `Sleep` replaces the wait between retries. `OnEvent` receives the method, path template, status and attempt, never JQL or bodies.
- `SearchAll(ctx, jql string, fields []string, limit int) ([]Issue, error)` posts to `/rest/api/3/search/jql` with token pagination. `limit` ranges from 1 to 10000, and 0 means 1000.
- `Transitions(ctx, issueKey) ([]Transition, error)` lists the transitions of an issue.
- `DoTransition(ctx, issueKey, transitionID string, fields map[string]any) error` performs a transition by id.
- `TransitionByName(ctx, issueKey, name string) error` matches the transition name case-insensitively.
- `GetProperty(ctx, issueKey, propertyKey string, v any) error` and `SetProperty(ctx, issueKey, propertyKey string, v any) error` read and write issue properties.
- Types: `Issue{ID, Key string; Fields map[string]json.RawMessage}` and `Transition{ID, Name, To string}`.

## Errors

Sentinel errors are matched with `errors.Is`: `ErrInvalidBaseURL`, `ErrInvalidIssueKey`, `ErrInvalidPropertyKey`, `ErrInvalidPropertyValue`, `ErrInvalidArgument`, `ErrInvalidResponse`, `ErrNotFound`, `ErrTransitionNotFound`, `ErrRateLimited`, `ErrPaginationLoop` and `ErrResponseTooLarge`. A non-2xx response returns `*APIError{StatusCode int; Messages []string}`, which matches `ErrNotFound` for status 404. Each message is truncated to 200 runes. `APIError.Code()` returns the stable error code.

## Limits

- The base URL must use https. Plain http is accepted only for 127.0.0.1, ::1 or localhost.
- Issue keys must match `^[A-Z][A-Z0-9_]+-[1-9][0-9]*$`, or be numeric ids matching `^[1-9][0-9]*$`.
- Property keys have 1 to 255 runes and are path-escaped. Property values are limited to 32768 bytes.
- Response bodies are read up to 10 MiB. A larger body returns `ErrResponseTooLarge`.
- HTTP 429 is always retried. HTTP 503 is retried only for GET, PUT and search requests. The delay is the Retry-After value when it is a non-negative integer number of seconds; otherwise it is 1s, 2s and 4s. Delays are capped at 60s.
- The default HTTP client has a 30s timeout and does not follow redirects, so credentials are never sent to a redirect target.

## Why not go-jira

go-jira and similar clients are large and are built around the deprecated search endpoint. jira-cloud-lite exposes only the search, transition and issue property operations listed above.

## Documentation

- [Architecture](docs/ARCHITECTURE.md) and [decisions](docs/decisions/)
- [Testing](docs/TESTING.md) and [security](docs/SECURITY.md)
- [Project record](docs/PROJECT.md) and [stack](docs/STACK.md)
- [Changelog](CHANGELOG.md) and [contributing guide](CONTRIBUTING.md)

## License

[MIT](LICENSE) (c) 2026 Miguel Gomez
