// Package jiracloudlite is a minimal client for the Atlassian Jira Cloud
// platform REST API v3. It covers enhanced JQL search, issue transitions and
// issue properties, and nothing else.
//
// Source of the endpoints: the Atlassian Jira Cloud platform REST API v3
// reference at https://developer.atlassian.com/cloud/jira/platform/rest/v3/
// (checked 2025-07-01). Search uses POST /rest/api/3/search/jql, which
// replaces the deprecated /rest/api/3/search endpoints; transitions use GET
// and POST /rest/api/3/issue/{issueIdOrKey}/transitions; properties use GET
// and PUT /rest/api/3/issue/{issueIdOrKey}/properties/{propertyKey}, whose
// values are limited to 32768 bytes.
//
// Create a client with New, passing the site URL, a BasicAuth (account
// e-mail and API token) and Options. Then call SearchAll, Transitions,
// DoTransition, TransitionByName, GetProperty and SetProperty. HTTP is
// performed through the Doer interface so tests never need a network.
//
// Safety properties: the base URL must be https (http only for loopback);
// the default HTTP client never follows redirects, so credentials never reach
// another host; issue keys and property keys are validated before any request
// and property keys are path-escaped; response bodies are limited to 10 MiB;
// 429 is always retried and 503 only for idempotent calls, honouring a
// Retry-After header given as delay-seconds (RFC 9110 section 10.2.3) and
// capped at 60 seconds; the token is never printed by any formatting verb and
// never appears in errors or in OnEvent notifications.
//
// Every error is an *Error sentinel with a stable Code, or an *APIError for
// a non-2xx answer. Use errors.Is with ErrNotFound, ErrRateLimited and the
// other sentinels. OAuth 2.0 (3LO), issue creation or editing, comments,
// Atlassian Document Format, agile and webhook APIs are out of scope.
package jiracloudlite
