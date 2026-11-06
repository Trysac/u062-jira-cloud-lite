# Testing

## Running

```sh
go test -cover .          # tests with coverage summary
go run ./scripts/check    # every gate, as CI and the factory run it
```

## Policy

- Statement coverage of the library (every package except `scripts/`) must stay at or above 80 %. The gate fails below that value.
- Tests live in package `jiracloudlite_test` and exercise the public API only. They assert return values, returned errors (`errors.Is`) and error messages.
- Table-driven tests use `t.Run` subtests. Usage is documented with runnable `Example` functions, which `go test` verifies through their `// Output:` comments.
- The race detector runs when cgo and a C compiler are available, which is always the case in Linux CI. The gate prints the reason when it runs without the race detector.
- Each bug fix starts with a failing test that reproduces it.
- Tests are deterministic: they use no network (except one loopback httptest server for the redirect case), no wall clock, and no dependence on test order. Retry waits are observed through a fake `Options.Sleep`.

## What the tests cover

- Pagination: `SearchAll` requests across pages until `isLast` is true, respects the limit, and returns `ErrPaginationLoop` when a token repeats.
- Transitions: name matching is case-insensitive, `ErrTransitionNotFound` lists the available names sorted, and a 503 on a transition is sent once.
- Retries: Retry-After handling (integer seconds, capping at 60s, fallback to 1s, 2s and 4s), `ErrRateLimited` after MaxRetries, and cancellation with `context.Canceled`.
- Errors: `*APIError` with status code and messages, `ErrNotFound` for 404, and redaction of the token in formatted output.
- Validation: issue keys, property keys and values are rejected before any request is sent. Property keys are escaped correctly.
- Client configuration: base URL rules and the refusal to follow a 302 redirect.
- Limits and events: the 10 MiB body limit and the content of `OnEvent` calls.

## Layout

Test files are named after the source file they cover, with the suffix `_test.go`, and sit in the same directory. Fixtures and golden files go in `testdata/`.
