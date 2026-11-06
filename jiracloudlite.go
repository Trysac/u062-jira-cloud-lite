package jiracloudlite

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxResponseBytes = 10 << 20
const readLimit = maxResponseBytes + 1
const maxPropertyValueBytes = 32768
const maxRequestBytes = 1 << 20
const maxJQLBytes = 32768
const maxFields = 100
const maxFieldNameBytes = 255
const maxPropertyKeyRunes = 255
const maxPropertyKeyBytes = 1100
const maxIdentifierBytes = 255
const maxBaseURLBytes = 2048
const maxCredentialBytes = 512
const maxUserAgentBytes = 200
const maxMessages = 20
const maxMessageRunes = 200
const maxErrorMessagesShown = 3
const maxTokenBytes = 8192
const maxPages = 1000
const pageSize = 100
const defaultLimit = 1000
const maxLimit = 10000
const defaultMaxRetries = 3
const maxRetriesCeiling = 10
const maxRetryDelay = 60 * time.Second
const defaultTimeout = 30 * time.Second
const defaultUserAgent = "jiracloudlite/1.0"
const maxListedNames = 50
const maxListedNameRunes = 100

const tplSearch = "/rest/api/3/search/jql"
const tplTransitions = "/rest/api/3/issue/{issueIdOrKey}/transitions"
const tplProperty = "/rest/api/3/issue/{issueIdOrKey}/properties/{propertyKey}"

var issueKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-[1-9][0-9]*$`)
var issueIDRE = regexp.MustCompile(`^[1-9][0-9]*$`)
var transitionIDRE = regexp.MustCompile(`^[0-9]{1,20}$`)

// Error is the base error type of the library. Every sentinel error below is
// an *Error, so callers can use errors.Is with a sentinel or errors.As with
// *Error and switch on the stable, machine-readable Code field.
type Error struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

func newError(code, message string) *Error {
	msg := "jiracloudlite: " + message
	return &Error{Code: code, Message: msg}
}

func wrap(sentinel *Error, reason string) error {
	return fmt.Errorf("%w: %s", sentinel, reason)
}

// ErrInvalidBaseURL is returned by New for a base URL that is not https (or http on loopback).
var ErrInvalidBaseURL = newError("invalid_base_url", "invalid base URL")

// ErrInvalidIssueKey is returned for an issue key or id that does not match the accepted syntax.
var ErrInvalidIssueKey = newError("invalid_issue_key", "invalid issue key")

// ErrInvalidPropertyKey is returned for a property key that is empty, too long or not valid UTF-8.
var ErrInvalidPropertyKey = newError("invalid_property_key", "invalid property key")

// ErrInvalidPropertyValue is returned for a property value that cannot be encoded or exceeds 32768 bytes.
var ErrInvalidPropertyValue = newError("invalid_property_value", "invalid property value")

// ErrNotFound matches an *APIError with status 404.
var ErrNotFound = newError("not_found", "resource not found")

// ErrTransitionNotFound is returned by TransitionByName when no transition has the requested name.
var ErrTransitionNotFound = newError("transition_not_found", "transition not found")

// ErrRateLimited matches an *APIError with status 429 once retries are exhausted.
var ErrRateLimited = newError("rate_limited", "rate limited")

// ErrPaginationLoop is returned when the search endpoint repeats a page token or never ends.
var ErrPaginationLoop = newError("pagination_loop", "pagination did not terminate")

// ErrResponseTooLarge is returned when a response body exceeds 10 MiB.
var ErrResponseTooLarge = newError("response_too_large", "response body exceeds 10 MiB")

// ErrInvalidArgument is returned for any other argument or option rejected before a request is sent.
var ErrInvalidArgument = newError("invalid_argument", "invalid argument")

// ErrInvalidResponse is returned when a successful response cannot be decoded as expected.
var ErrInvalidResponse = newError("invalid_response", "invalid response")

// APIError describes a non-2xx response. Messages holds the entries of the
// "errorMessages" array and the "errors" object (as "field: message"), each
// truncated to 200 runes. It matches ErrNotFound for status 404 and
// ErrRateLimited for status 429.
type APIError struct {
	StatusCode int
	Messages   []string
}

// Error implements the error interface. It shows at most three messages.
func (e *APIError) Error() string {
	s := "jiracloudlite: jira returned HTTP " + strconv.Itoa(e.StatusCode)
	if len(e.Messages) > 0 {
		shown := e.Messages[:min(len(e.Messages), maxErrorMessagesShown)]
		s += ": " + strings.Join(shown, "; ")
	}
	return s
}

// Is reports whether target is ErrNotFound (status 404) or ErrRateLimited (status 429).
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// Code returns the stable machine-readable code of an API error.
func (e *APIError) Code() string { return "api_error" }

// Doer sends an HTTP request. *http.Client implements it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// BasicAuth holds the Atlassian account e-mail and API token used for HTTP
// Basic authentication. Its formatting methods never reveal the token.
type BasicAuth struct {
	Email    string
	APIToken string
}

// String returns the e-mail and a masked token.
func (a BasicAuth) String() string {
	return "BasicAuth{Email: " + a.Email + ", APIToken: ***}"
}

// GoString returns the e-mail and a masked token for %#v.
func (a BasicAuth) GoString() string {
	return fmt.Sprintf("jiracloudlite.BasicAuth{Email:%q, APIToken:\"***\"}", a.Email)
}

type secret string

func (secret) String() string   { return "***" }
func (secret) GoString() string { return "***" }

// Options configures a Client. The zero value is valid.
type Options struct {
	// Doer sends requests. The default is an http.Client with a 30 second
	// timeout that never follows redirects, so credentials cannot leak to
	// another host. A custom Doer must provide the same guarantee.
	Doer Doer

	// Sleep waits between retries and must return early with the context
	// error when the context ends. Tests inject a fake; the default uses a timer.
	Sleep func(context.Context, time.Duration) error

	// MaxRetries is the number of retries after the first attempt. Zero means
	// the default of 3, a negative value disables retries, and the maximum is 10.
	MaxRetries int

	// UserAgent overrides the User-Agent header.
	UserAgent string

	// OnEvent is called after every attempt with the method, the path
	// template (never the real path, JQL or bodies), the HTTP status (0 on
	// transport failure) and the 1-based attempt number.
	OnEvent func(method, pathTemplate string, status, attempt int)
}

// Client is a Jira Cloud REST v3 client. It is safe for concurrent use.
type Client struct {
	root       string
	auth       secret
	doer       Doer
	sleep      func(context.Context, time.Duration) error
	maxRetries int
	userAgent  string
	onEvent    func(method, pathTemplate string, status, attempt int)
}

// String returns the base URL only; credentials are never included.
func (c Client) String() string { return "jiracloudlite.Client{" + c.root + "}" }

// GoString returns the same text as String.
func (c Client) GoString() string { return c.String() }

// Issue is a Jira issue as returned by search. Fields keeps the raw JSON of each requested field.
type Issue struct {
	ID     string                     `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// Transition is a workflow transition available for an issue; To is the target status name.
type Transition struct {
	ID   string
	Name string
	To   string
}

func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func defaultDoer() Doer {
	client := &http.Client{Timeout: defaultTimeout}
	client.CheckRedirect = noRedirect
	return client
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isLoopback(host string) bool {
	h := strings.ToLower(host)
	return h == "127.0.0.1" || h == "::1" || h == "localhost"
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func parseBaseURL(raw string) (string, error) {
	if raw == "" || len(raw) > maxBaseURLBytes {
		return "", wrap(ErrInvalidBaseURL, "base URL is empty or too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", wrap(ErrInvalidBaseURL, "base URL must be absolute without credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", wrap(ErrInvalidBaseURL, "plain http is only allowed for loopback hosts")
		}
	default:
		return "", wrap(ErrInvalidBaseURL, "base URL scheme must be https")
	}
	prefix := strings.TrimRight(u.EscapedPath(), "/")
	root := u.Scheme + "://" + u.Host + prefix
	return root, nil
}

func validateCredentials(auth BasicAuth) error {
	if auth.Email == "" || auth.APIToken == "" {
		return wrap(ErrInvalidArgument, "e-mail and API token are required")
	}
	if len(auth.Email) > maxCredentialBytes || len(auth.APIToken) > maxCredentialBytes {
		return wrap(ErrInvalidArgument, "credentials exceed 512 bytes")
	}
	if strings.Contains(auth.Email, ":") || hasControl(auth.Email) || hasControl(auth.APIToken) {
		return wrap(ErrInvalidArgument, "credentials contain forbidden characters")
	}
	return nil
}

// New creates a Client for the Jira Cloud site at baseURL. The URL must be
// https, or http for 127.0.0.1, ::1 or localhost, and must not carry
// credentials, a query or a fragment.
func New(baseURL string, auth BasicAuth, opts Options) (*Client, error) {
	root, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if err := validateCredentials(auth); err != nil {
		return nil, err
	}
	retries := opts.MaxRetries
	switch {
	case retries == 0:
		retries = defaultMaxRetries
	case retries < 0:
		retries = 0
	case retries > maxRetriesCeiling:
		return nil, wrap(ErrInvalidArgument, "MaxRetries exceeds the maximum of 10")
	}
	agent := opts.UserAgent
	if agent == "" {
		agent = defaultUserAgent
	}
	if len(agent) > maxUserAgentBytes || hasControl(agent) {
		return nil, wrap(ErrInvalidArgument, "invalid User-Agent")
	}
	doer := opts.Doer
	if doer == nil {
		doer = defaultDoer()
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	raw := auth.Email + ":" + auth.APIToken
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	c := &Client{}
	c.root = root
	c.auth = secret(header)
	c.doer = doer
	c.sleep = sleep
	c.maxRetries = retries
	c.userAgent = agent
	c.onEvent = opts.OnEvent
	return c, nil
}

type call struct {
	method   string
	template string
	path     string
	body     []byte
	retry503 bool
}

type response struct {
	status int
	header http.Header
	body   []byte
}

type errorPayload struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

func (c *Client) emit(cl call, status, attempt int) {
	if c.onEvent != nil {
		c.onEvent(cl.method, cl.template, status, attempt)
	}
}

func (c *Client) send(ctx context.Context, cl call) (response, error) {
	target := c.root + cl.path
	var reader io.Reader
	if cl.body != nil {
		reader = bytes.NewReader(cl.body)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, target, reader)
	if err != nil {
		return response{}, wrap(ErrInvalidArgument, "cannot build request")
	}
	req.Header.Set("Authorization", string(c.auth))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if cl.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("jiracloudlite: request failed: %w", err)
	}
	if resp == nil {
		return response{}, errors.New("jiracloudlite: doer returned no response")
	}
	out := response{status: resp.StatusCode, header: resp.Header}
	if resp.Body == nil {
		return out, nil
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return out, fmt.Errorf("jiracloudlite: reading response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return out, wrap(ErrResponseTooLarge, "limit is 10 MiB")
	}
	out.body = data
	return out, nil
}

func parseDelaySeconds(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n > 60 {
		return maxRetryDelay, true
	}
	d := time.Duration(n) * time.Second
	return d, true
}

func retryDelay(header string, attempt int) time.Duration {
	if d, ok := parseDelaySeconds(header); ok {
		return d
	}
	shift := attempt - 1
	if shift >= 6 {
		return maxRetryDelay
	}
	return time.Second << shift
}

func truncateRunes(s string, limit int) string {
	count := 0
	for i := range s {
		if count == limit {
			return s[:i]
		}
		count++
	}
	return s
}

func newAPIError(status int, body []byte) *APIError {
	e := &APIError{StatusCode: status}
	var p errorPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return e
	}
	add := func(m string) {
		if m == "" || len(e.Messages) >= maxMessages {
			return
		}
		e.Messages = append(e.Messages, truncateRunes(m, maxMessageRunes))
	}
	for _, m := range p.ErrorMessages {
		add(m)
	}
	keys := make([]string, 0, len(p.Errors))
	for k := range p.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k + ": " + p.Errors[k])
	}
	return e
}

func decode(data []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return wrap(ErrInvalidResponse, "response is not the expected JSON")
	}
	return nil
}

func (c *Client) do(ctx context.Context, cl call, out any) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.send(ctx, cl)
		c.emit(cl, resp.status, attempt)
		if err != nil {
			return err
		}
		retryable := resp.status == http.StatusTooManyRequests
		if resp.status == http.StatusServiceUnavailable && cl.retry503 {
			retryable = true
		}
		if retryable && attempt <= c.maxRetries {
			delay := retryDelay(resp.header.Get("Retry-After"), attempt)
			if err := c.sleep(ctx, delay); err != nil {
				return fmt.Errorf("jiracloudlite: waiting to retry: %w", err)
			}
			continue
		}
		if resp.status < 200 || resp.status > 299 {
			return newAPIError(resp.status, resp.body)
		}
		return decode(resp.body, out)
	}
}

func validIssueKey(key string) bool {
	if key == "" || len(key) > maxIdentifierBytes {
		return false
	}
	return issueKeyRE.MatchString(key) || issueIDRE.MatchString(key)
}

func issuePath(key string) (string, error) {
	if !validIssueKey(key) {
		return "", wrap(ErrInvalidIssueKey, "expected a key such as ABC-1 or a numeric id")
	}
	p := "/rest/api/3/issue/" + key
	return p, nil
}

func propertyPath(issueKey, propertyKey string) (string, error) {
	p, err := issuePath(issueKey)
	if err != nil {
		return "", err
	}
	if propertyKey == "" || len(propertyKey) > maxPropertyKeyBytes || !utf8.ValidString(propertyKey) || utf8.RuneCountInString(propertyKey) > maxPropertyKeyRunes {
		return "", wrap(ErrInvalidPropertyKey, "property key must be 1 to 255 valid UTF-8 characters")
	}
	full := p + "/properties/" + url.PathEscape(propertyKey)
	return full, nil
}

func marshalBounded(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, wrap(ErrInvalidArgument, "request cannot be encoded as JSON")
	}
	if len(body) > maxRequestBytes {
		return nil, wrap(ErrInvalidArgument, "request body exceeds 1 MiB")
	}
	return body, nil
}

type searchRequest struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields,omitempty"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

type searchResponse struct {
	Issues        []Issue `json:"issues"`
	NextPageToken string  `json:"nextPageToken"`
	IsLast        bool    `json:"isLast"`
}

// SearchAll runs an enhanced JQL search (POST /rest/api/3/search/jql) and
// follows nextPageToken until isLast, the limit, or the end of results. The
// limit must be between 1 and 10000 (0 means 1000). A repeated page token, or
// more than 1000 pages, returns ErrPaginationLoop. Issues are returned in the
// order Jira sent them, truncated to the limit.
func (c *Client) SearchAll(ctx context.Context, jql string, fields []string, limit int) ([]Issue, error) {
	if strings.TrimSpace(jql) == "" || len(jql) > maxJQLBytes {
		return nil, wrap(ErrInvalidArgument, "JQL must be non-blank and at most 32768 bytes")
	}
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return nil, wrap(ErrInvalidArgument, "limit must be between 1 and 10000")
	}
	if len(fields) > maxFields {
		return nil, wrap(ErrInvalidArgument, "at most 100 fields are allowed")
	}
	for _, f := range fields {
		if f == "" || len(f) > maxFieldNameBytes {
			return nil, wrap(ErrInvalidArgument, "field names must be 1 to 255 bytes")
		}
	}
	seen := make(map[string]struct{})
	token := ""
	out := make([]Issue, 0, min(limit, pageSize))
	for page := 0; page < maxPages; page++ {
		body, err := marshalBounded(searchRequest{JQL: jql, Fields: fields, MaxResults: pageSize, NextPageToken: token})
		if err != nil {
			return nil, err
		}
		var resp searchResponse
		cl := call{method: http.MethodPost, template: tplSearch, path: tplSearch, body: body, retry503: true}
		if err := c.do(ctx, cl, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Issues...)
		if len(out) >= limit {
			return out[:limit], nil
		}
		if resp.IsLast || resp.NextPageToken == "" {
			return out, nil
		}
		if len(resp.NextPageToken) > maxTokenBytes {
			return nil, wrap(ErrInvalidResponse, "page token is too long")
		}
		if _, dup := seen[resp.NextPageToken]; dup {
			return nil, wrap(ErrPaginationLoop, "page token repeated")
		}
		seen[resp.NextPageToken] = struct{}{}
		token = resp.NextPageToken
	}
	return nil, wrap(ErrPaginationLoop, "too many pages")
}

type transitionTarget struct {
	Name string `json:"name"`
}

type transitionWire struct {
	ID   string           `json:"id"`
	Name string           `json:"name"`
	To   transitionTarget `json:"to"`
}

type transitionsResponse struct {
	Transitions []transitionWire `json:"transitions"`
}

// Transitions lists the transitions currently available for the issue.
func (c *Client) Transitions(ctx context.Context, issueKey string) ([]Transition, error) {
	p, err := issuePath(issueKey)
	if err != nil {
		return nil, err
	}
	target := p + "/transitions"
	var resp transitionsResponse
	cl := call{method: http.MethodGet, template: tplTransitions, path: target, retry503: true}
	if err := c.do(ctx, cl, &resp); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(resp.Transitions))
	for _, t := range resp.Transitions {
		out = append(out, Transition{ID: t.ID, Name: t.Name, To: t.To.Name})
	}
	return out, nil
}

type transitionRef struct {
	ID string `json:"id"`
}

type transitionRequest struct {
	Transition transitionRef  `json:"transition"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// DoTransition performs the transition with the given numeric id, optionally
// setting fields. It is sent exactly once on a 5xx answer, because a
// transition is not idempotent; only 429 (the request was rejected) is retried.
func (c *Client) DoTransition(ctx context.Context, issueKey, transitionID string, fields map[string]any) error {
	p, err := issuePath(issueKey)
	if err != nil {
		return err
	}
	if !transitionIDRE.MatchString(transitionID) {
		return wrap(ErrInvalidArgument, "transition id must be 1 to 20 digits")
	}
	body, err := marshalBounded(transitionRequest{Transition: transitionRef{ID: transitionID}, Fields: fields})
	if err != nil {
		return err
	}
	target := p + "/transitions"
	cl := call{method: http.MethodPost, template: tplTransitions, path: target, body: body}
	return c.do(ctx, cl, nil)
}

// TransitionByName finds the transition whose name equals name, ignoring
// case, and performs it. When none matches it returns ErrTransitionNotFound
// with the sorted list of available names in the message.
func (c *Client) TransitionByName(ctx context.Context, issueKey, name string) error {
	if name == "" || len(name) > maxFieldNameBytes {
		return wrap(ErrInvalidArgument, "transition name must be 1 to 255 bytes")
	}
	list, err := c.Transitions(ctx, issueKey)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(list))
	for _, t := range list {
		if strings.EqualFold(t.Name, name) {
			return c.DoTransition(ctx, issueKey, t.ID, nil)
		}
		names = append(names, truncateRunes(t.Name, maxListedNameRunes))
	}
	sort.Strings(names)
	listed := names
	extra := 0
	if len(names) > maxListedNames {
		listed = names[:maxListedNames]
		extra = len(names) - maxListedNames
	}
	available := strings.Join(listed, ", ")
	if available == "" {
		available = "(none)"
	}
	msg := "no transition named " + strconv.Quote(truncateRunes(name, maxListedNameRunes)) + "; available: " + available
	if extra > 0 {
		msg += " (and " + strconv.Itoa(extra) + " more)"
	}
	return wrap(ErrTransitionNotFound, msg)
}

type propertyResponse struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// GetProperty reads an issue property and decodes its JSON value into v,
// which must be a non-nil pointer.
func (c *Client) GetProperty(ctx context.Context, issueKey, propertyKey string, v any) error {
	p, err := propertyPath(issueKey, propertyKey)
	if err != nil {
		return err
	}
	if v == nil {
		return wrap(ErrInvalidArgument, "destination must not be nil")
	}
	var resp propertyResponse
	cl := call{method: http.MethodGet, template: tplProperty, path: p, retry503: true}
	if err := c.do(ctx, cl, &resp); err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Value, v); err != nil {
		return wrap(ErrInvalidResponse, "property value does not match the destination")
	}
	return nil
}

// SetProperty stores v, encoded as JSON, as an issue property. The encoded
// value must not exceed 32768 bytes. The call is idempotent and is retried on 503.
func (c *Client) SetProperty(ctx context.Context, issueKey, propertyKey string, v any) error {
	p, err := propertyPath(issueKey, propertyKey)
	if err != nil {
		return err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return wrap(ErrInvalidPropertyValue, "value cannot be encoded as JSON")
	}
	if len(body) > maxPropertyValueBytes {
		return wrap(ErrInvalidPropertyValue, "encoded value exceeds 32768 bytes")
	}
	cl := call{method: http.MethodPut, template: tplProperty, path: p, body: body, retry503: true}
	return c.do(ctx, cl, nil)
}
