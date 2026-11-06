package jiracloudlite_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jiracloudlite "github.com/Trysac/u062-jira-cloud-lite"
)

const testToken = "fake" // test value, not a credential
const testEmail = "bot@example.invalid"
const testBase = "https://example.atlassian.net"
const testCreds = testEmail + ":" + testToken
const transitionsJSON = `{"transitions":[{"id":"11","name":"To Do","to":{"name":"To Do"}},{"id":"21","name":"In Progress","to":{"name":"In Progress"}}]}`
const transitionsTemplate = "/rest/api/3/issue/{issueIdOrKey}/transitions"

var bg = context.Background()

type handlerFunc = func(int, *http.Request) (*http.Response, error)

type rec struct {
	method string
	path   string
	body   string
	header http.Header
}

type fakeDoer struct {
	handler handlerFunc
	calls   []rec
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	f.calls = append(f.calls, rec{r.Method, r.URL.EscapedPath(), body, r.Header.Clone()})
	return f.handler(len(f.calls), r)
}

type sleeper struct {
	delays []time.Duration
}

func (s *sleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return ctx.Err()
}

type event struct {
	method   string
	template string
	status   int
	attempt  int
}

type propValue struct {
	N int `json:"n"`
}

type errReader struct{}

var errBoom = errors.New("boom")

func (errReader) Read([]byte) (int, error) { return 0, errBoom }

func reply(status int, body string, kv ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func always(status int, body string, kv ...string) handlerFunc {
	return func(int, *http.Request) (*http.Response, error) {
		return reply(status, body, kv...), nil
	}
}

func mk(t *testing.T, h handlerFunc, opts jiracloudlite.Options) (*jiracloudlite.Client, *fakeDoer, *sleeper) {
	t.Helper()
	d := &fakeDoer{handler: h}
	sl := &sleeper{}
	opts.Doer = d
	if opts.Sleep == nil {
		opts.Sleep = sl.Sleep
	}
	c, err := jiracloudlite.New(testBase, jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, d, sl
}

func expectErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want match for %v", err, target)
	}
}

func errOf(_ []jiracloudlite.Issue, err error) error { return err }

func TestNewBaseURL(t *testing.T) {
	auth := jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}
	valid := []string{"https://example.atlassian.net", "https://example.atlassian.net/", "http://127.0.0.1:8080", "http://localhost", "http://[::1]:8080", "https://example.com/jira/"}
	for _, u := range valid {
		if _, err := jiracloudlite.New(u, auth, jiracloudlite.Options{}); err != nil {
			t.Errorf("New(%q) = %v, want nil", u, err)
		}
	}
	long := "https://" + strings.Repeat("a", 3000)
	invalid := []string{"http://jira.example.com", "", "ftp://example.com", "https://", "https://user:pw@example.com", "https://example.com?q=1", "https://example.com#frag", "://bad", "http://127.0.0.1.evil.example", "http://localhost.evil.example", "example.com", long}
	for _, u := range invalid {
		_, err := jiracloudlite.New(u, auth, jiracloudlite.Options{})
		if !errors.Is(err, jiracloudlite.ErrInvalidBaseURL) {
			t.Errorf("New(%.40q) error = %v, want ErrInvalidBaseURL", u, err)
		}
	}
}

func TestNewOptionErrors(t *testing.T) {
	bad := []jiracloudlite.BasicAuth{{Email: "", APIToken: "x"}, {Email: testEmail, APIToken: ""}, {Email: "a:b@example.invalid", APIToken: "x"}, {Email: "a\n@example.invalid", APIToken: "x"}, {Email: testEmail, APIToken: strings.Repeat("x", 600)}}
	for _, a := range bad {
		_, err := jiracloudlite.New(testBase, a, jiracloudlite.Options{})
		expectErr(t, err, jiracloudlite.ErrInvalidArgument)
	}
	longAgent := strings.Repeat("u", 300)
	good := jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}
	for _, o := range []jiracloudlite.Options{{MaxRetries: 11}, {UserAgent: "a\nb"}, {UserAgent: longAgent}} {
		_, err := jiracloudlite.New(testBase, good, o)
		expectErr(t, err, jiracloudlite.ErrInvalidArgument)
	}
}

func TestSearchAllPaginates(t *testing.T) {
	pages := []string{
		`{"issues":[{"id":"1","key":"ABC-1","fields":{"summary":"one"}}],"nextPageToken":"t1","isLast":false}`,
		`{"issues":[{"id":"2","key":"ABC-2","fields":{}}],"nextPageToken":"t2","isLast":false}`,
		`{"issues":[{"id":"3","key":"ABC-3"}],"isLast":true}`,
	}
	h := func(n int, _ *http.Request) (*http.Response, error) {
		return reply(200, pages[n-1]), nil
	}
	c, d, _ := mk(t, h, jiracloudlite.Options{})
	issues, err := c.SearchAll(bg, "project = ABC", []string{"summary"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 3 || issues[0].Key != "ABC-1" || issues[1].Key != "ABC-2" || issues[2].Key != "ABC-3" {
		t.Fatalf("issues = %+v", issues)
	}
	if string(issues[0].Fields["summary"]) != `"one"` {
		t.Fatalf("summary = %s", issues[0].Fields["summary"])
	}
	if len(d.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(d.calls))
	}
	wantTokens := []string{"", "t1", "t2"}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(testCreds))
	for i, cl := range d.calls {
		if cl.method != "POST" || cl.path != "/rest/api/3/search/jql" {
			t.Fatalf("call %d = %s %s", i, cl.method, cl.path)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(cl.body), &body); err != nil {
			t.Fatal(err)
		}
		if body["jql"] != "project = ABC" || body["maxResults"] != float64(100) {
			t.Fatalf("body = %v", body)
		}
		fields, _ := body["fields"].([]any)
		if len(fields) != 1 || fields[0] != "summary" {
			t.Fatalf("fields = %v", body["fields"])
		}
		tok, present := body["nextPageToken"]
		if wantTokens[i] == "" {
			if present {
				t.Fatalf("first page sent a token: %v", tok)
			}
		} else if tok != wantTokens[i] {
			t.Fatalf("token = %v, want %s", tok, wantTokens[i])
		}
		if cl.header.Get("Authorization") != wantAuth || cl.header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers = %v", cl.header)
		}
	}
}

func TestSearchAllLimit(t *testing.T) {
	page := `{"issues":[{"id":"1","key":"ABC-1"},{"id":"2","key":"ABC-2"},{"id":"3","key":"ABC-3"}],"nextPageToken":"t1","isLast":false}`
	c, d, _ := mk(t, always(200, page), jiracloudlite.Options{})
	issues, err := c.SearchAll(bg, "project = ABC", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[1].Key != "ABC-2" || len(d.calls) != 1 {
		t.Fatalf("issues = %+v calls = %d", issues, len(d.calls))
	}
}

func TestSearchAllEmptyToken(t *testing.T) {
	c, d, _ := mk(t, always(200, `{"issues":[{"id":"1","key":"ABC-1"}],"isLast":false}`), jiracloudlite.Options{})
	issues, err := c.SearchAll(bg, "project = ABC", nil, 10)
	if err != nil || len(issues) != 1 || len(d.calls) != 1 {
		t.Fatalf("issues = %+v err = %v calls = %d", issues, err, len(d.calls))
	}
}

func TestSearchAllRepeatedToken(t *testing.T) {
	body := `{"issues":[],"nextPageToken":"t1","isLast":false}`
	c, d, _ := mk(t, always(200, body), jiracloudlite.Options{})
	_, err := c.SearchAll(bg, "project = ABC", nil, 0)
	expectErr(t, err, jiracloudlite.ErrPaginationLoop)
	if len(d.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(d.calls))
	}
}

func TestSearchAllTooManyPages(t *testing.T) {
	h := func(n int, _ *http.Request) (*http.Response, error) {
		body := `{"issues":[],"nextPageToken":"` + strconv.Itoa(n) + `","isLast":false}`
		return reply(200, body), nil
	}
	c, d, _ := mk(t, h, jiracloudlite.Options{})
	_, err := c.SearchAll(bg, "project = ABC", nil, 0)
	expectErr(t, err, jiracloudlite.ErrPaginationLoop)
	if len(d.calls) != 1000 {
		t.Fatalf("calls = %d, want 1000", len(d.calls))
	}
}

func TestSearchAllLongToken(t *testing.T) {
	token := strings.Repeat("t", 9000)
	body := `{"issues":[],"nextPageToken":"` + token + `","isLast":false}`
	c, _, _ := mk(t, always(200, body), jiracloudlite.Options{})
	_, err := c.SearchAll(bg, "project = ABC", nil, 0)
	expectErr(t, err, jiracloudlite.ErrInvalidResponse)
}

func TestSearchAllValidation(t *testing.T) {
	c, d, _ := mk(t, always(200, `{"issues":[],"isLast":true}`), jiracloudlite.Options{})
	longJQL := strings.Repeat("a", 40000)
	many := make([]string, 101)
	for i := range many {
		many[i] = "f"
	}
	longField := strings.Repeat("f", 300)
	expectErr(t, errOf(c.SearchAll(bg, "", nil, 0)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "   ", nil, 0)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, longJQL, nil, 0)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "project = ABC", nil, -1)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "project = ABC", nil, 10001)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "project = ABC", many, 0)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "project = ABC", []string{""}, 0)), jiracloudlite.ErrInvalidArgument)
	expectErr(t, errOf(c.SearchAll(bg, "project = ABC", []string{longField}, 0)), jiracloudlite.ErrInvalidArgument)
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
	if _, err := c.SearchAll(bg, "project = ABC", nil, 10000); err != nil {
		t.Fatal(err)
	}
}

func TestSearchAllServerError(t *testing.T) {
	c, _, _ := mk(t, always(400, `{"errorMessages":["bad jql"]}`), jiracloudlite.Options{})
	_, err := c.SearchAll(bg, "project = ABC", nil, 0)
	var apiErr *jiracloudlite.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("error = %v", err)
	}
}

func TestTransitionsList(t *testing.T) {
	c, d, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{})
	list, err := c.Transitions(bg, "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].ID != "21" || list[1].Name != "In Progress" || list[1].To != "In Progress" {
		t.Fatalf("list = %+v", list)
	}
	if d.calls[0].method != "GET" || d.calls[0].path != "/rest/api/3/issue/ABC-1/transitions" {
		t.Fatalf("call = %+v", d.calls[0])
	}
}

func TestTransitionByName(t *testing.T) {
	h := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(200, transitionsJSON), nil
		}
		return reply(204, ""), nil
	}
	c, d, _ := mk(t, h, jiracloudlite.Options{})
	if err := c.TransitionByName(bg, "ABC-1", "in progress"); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || d.calls[0].method != "GET" || d.calls[1].method != "POST" {
		t.Fatalf("calls = %+v", d.calls)
	}
	if d.calls[1].path != "/rest/api/3/issue/ABC-1/transitions" || d.calls[1].body != `{"transition":{"id":"21"}}` {
		t.Fatalf("post = %+v", d.calls[1])
	}
}

func TestTransitionByNameNotFound(t *testing.T) {
	c, d, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{})
	err := c.TransitionByName(bg, "ABC-1", "Done")
	expectErr(t, err, jiracloudlite.ErrTransitionNotFound)
	if !strings.Contains(err.Error(), "In Progress, To Do") {
		t.Fatalf("message does not list sorted names: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
	empty, _, _ := mk(t, always(200, `{"transitions":[]}`), jiracloudlite.Options{})
	err = empty.TransitionByName(bg, "ABC-1", "Done")
	expectErr(t, err, jiracloudlite.ErrTransitionNotFound)
	if !strings.Contains(err.Error(), "(none)") {
		t.Fatalf("message = %v", err)
	}
}

func TestTransitionByNameManyNames(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"transitions":[`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"%d","name":"T%02d","to":{"name":"X"}}`, i+1, i)
	}
	b.WriteString(`]}`)
	c, _, _ := mk(t, always(200, b.String()), jiracloudlite.Options{})
	err := c.TransitionByName(bg, "ABC-1", "zzz")
	expectErr(t, err, jiracloudlite.ErrTransitionNotFound)
	if !strings.Contains(err.Error(), "(and 10 more)") {
		t.Fatalf("message = %v", err)
	}
}

func TestTransitionByNameErrors(t *testing.T) {
	c, d, _ := mk(t, always(404, `{"errorMessages":["Issue does not exist"]}`), jiracloudlite.Options{})
	expectErr(t, c.TransitionByName(bg, "ABC-1", "Done"), jiracloudlite.ErrNotFound)
	expectErr(t, c.TransitionByName(bg, "ABC-1", ""), jiracloudlite.ErrInvalidArgument)
	expectErr(t, c.TransitionByName(bg, "ABC-1", strings.Repeat("n", 300)), jiracloudlite.ErrInvalidArgument)
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
}

func TestDoTransition(t *testing.T) {
	c, d, _ := mk(t, func(int, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204}, nil
	}, jiracloudlite.Options{})
	fields := map[string]any{"resolution": map[string]string{"name": "Done"}}
	if err := c.DoTransition(bg, "ABC-1", "31", fields); err != nil {
		t.Fatal(err)
	}
	want := `{"transition":{"id":"31"},"fields":{"resolution":{"name":"Done"}}}`
	if d.calls[0].body != want {
		t.Fatalf("body = %s", d.calls[0].body)
	}
	expectErr(t, c.DoTransition(bg, "ABC-1", "abc", nil), jiracloudlite.ErrInvalidArgument)
	expectErr(t, c.DoTransition(bg, "ABC-1", "", nil), jiracloudlite.ErrInvalidArgument)
	expectErr(t, c.DoTransition(bg, "ABC-1", "21", map[string]any{"x": make(chan int)}), jiracloudlite.ErrInvalidArgument)
	huge := map[string]any{"x": strings.Repeat("a", 2<<20)}
	expectErr(t, c.DoTransition(bg, "ABC-1", "21", huge), jiracloudlite.ErrInvalidArgument)
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
}

func TestRetryAfterHeaderHonoured(t *testing.T) {
	two := 2 * time.Second
	minute := 60 * time.Second
	h := func(ra string) handlerFunc {
		return func(n int, _ *http.Request) (*http.Response, error) {
			if n == 1 {
				return reply(429, "", "Retry-After", ra), nil
			}
			return reply(200, transitionsJSON), nil
		}
	}
	c, _, sl := mk(t, h("2"), jiracloudlite.Options{})
	if _, err := c.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if len(sl.delays) != 1 || sl.delays[0] != two {
		t.Fatalf("delays = %v, want [2s]", sl.delays)
	}
	c, _, sl = mk(t, h("120"), jiracloudlite.Options{})
	if _, err := c.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if len(sl.delays) != 1 || sl.delays[0] != minute {
		t.Fatalf("delays = %v, want [1m0s]", sl.delays)
	}
	for _, tc := range [][2]string{{"0", "0s"}, {"002", "2s"}, {"99999999999999999999999", "1m0s"}} {
		c, _, sl = mk(t, h(tc[0]), jiracloudlite.Options{})
		if _, err := c.Transitions(bg, "ABC-1"); err != nil {
			t.Fatal(err)
		}
		if len(sl.delays) != 1 || sl.delays[0].String() != tc[1] {
			t.Fatalf("Retry-After %q: delays = %v, want [%s]", tc[0], sl.delays, tc[1])
		}
	}
}

func TestRetryFallbackBackoff(t *testing.T) {
	s1 := time.Second
	s2 := 2 * time.Second
	s4 := 4 * time.Second
	want := []time.Duration{s1, s2, s4}
	for _, ra := range []string{"2.5", "-1", "Wed, 21 Oct 2015 07:28:00 GMT", "", "+2", " 2", "2s", "1e1"} {
		c, d, sl := mk(t, always(429, "", "Retry-After", ra), jiracloudlite.Options{})
		_, err := c.Transitions(bg, "ABC-1")
		expectErr(t, err, jiracloudlite.ErrRateLimited)
		if len(d.calls) != 4 || len(sl.delays) != 3 {
			t.Fatalf("Retry-After %q: calls = %d delays = %v", ra, len(d.calls), sl.delays)
		}
		for i := range want {
			if sl.delays[i] != want[i] {
				t.Fatalf("Retry-After %q: delays = %v, want %v", ra, sl.delays, want)
			}
		}
		var apiErr *jiracloudlite.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
			t.Fatalf("error = %v", err)
		}
		if errors.Is(err, jiracloudlite.ErrNotFound) {
			t.Fatal("429 must not match ErrNotFound")
		}
	}
}

func TestRetryBackoffCapped(t *testing.T) {
	c, d, sl := mk(t, always(429, ""), jiracloudlite.Options{MaxRetries: 10})
	_, err := c.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrRateLimited)
	if len(d.calls) != 11 || len(sl.delays) != 10 {
		t.Fatalf("calls = %d delays = %v", len(d.calls), sl.delays)
	}
	for i, got := range sl.delays {
		want := 60 * time.Second
		if i < 6 {
			want = time.Second << i
		}
		if got != want {
			t.Fatalf("delay %d = %v, want %v", i, got, want)
		}
	}
}

func TestRetriesDisabled(t *testing.T) {
	c, d, sl := mk(t, always(429, ""), jiracloudlite.Options{MaxRetries: -1})
	_, err := c.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrRateLimited)
	if len(d.calls) != 1 || len(sl.delays) != 0 {
		t.Fatalf("calls = %d delays = %v", len(d.calls), sl.delays)
	}
}

func TestRetryContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	sleepFn := func(c context.Context, _ time.Duration) error {
		cancel()
		return c.Err()
	}
	c, d, _ := mk(t, always(429, ""), jiracloudlite.Options{Sleep: sleepFn})
	_, err := c.Transitions(ctx, "ABC-1")
	expectErr(t, err, context.Canceled)
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
	c2, d2, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{})
	_, err = c2.Transitions(ctx, "ABC-1")
	expectErr(t, err, context.Canceled)
	if len(d2.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d2.calls))
	}
}

func TestRetrySleepError(t *testing.T) {
	sleepFn := func(context.Context, time.Duration) error { return errBoom }
	c, _, _ := mk(t, always(429, ""), jiracloudlite.Options{Sleep: sleepFn})
	_, err := c.Transitions(bg, "ABC-1")
	expectErr(t, err, errBoom)
}

func TestDefaultSleep(t *testing.T) {
	auth := jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}
	h := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(429, "", "Retry-After", "0"), nil
		}
		return reply(200, transitionsJSON), nil
	}
	d := &fakeDoer{handler: h}
	c, err := jiracloudlite.New(testBase, auth, jiracloudlite.Options{Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Transitions(bg, "ABC-1"); err != nil || len(d.calls) != 2 {
		t.Fatalf("err = %v calls = %d", err, len(d.calls))
	}
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	h2 := func(int, *http.Request) (*http.Response, error) {
		cancel()
		return reply(429, "", "Retry-After", "5"), nil
	}
	d2 := &fakeDoer{handler: h2}
	c2, err := jiracloudlite.New(testBase, auth, jiracloudlite.Options{Doer: d2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c2.Transitions(ctx, "ABC-1")
	expectErr(t, err, context.Canceled)
}

func TestRetry503OnlyForIdempotentCalls(t *testing.T) {
	h := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(503, ""), nil
		}
		return reply(200, transitionsJSON), nil
	}
	c, d, _ := mk(t, h, jiracloudlite.Options{})
	if _, err := c.Transitions(bg, "ABC-1"); err != nil || len(d.calls) != 2 {
		t.Fatalf("GET: err = %v calls = %d", err, len(d.calls))
	}

	c2, d2, sl2 := mk(t, always(503, ""), jiracloudlite.Options{})
	err := c2.DoTransition(bg, "ABC-1", "21", nil)
	var apiErr *jiracloudlite.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
		t.Fatalf("error = %v", err)
	}
	if len(d2.calls) != 1 || len(sl2.delays) != 0 {
		t.Fatalf("POST: calls = %d delays = %v", len(d2.calls), sl2.delays)
	}

	c3, d3, _ := mk(t, h, jiracloudlite.Options{})
	if err := c3.SetProperty(bg, "ABC-1", "k", 1); err != nil || len(d3.calls) != 2 {
		t.Fatalf("PUT: err = %v calls = %d", err, len(d3.calls))
	}

	search := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(503, ""), nil
		}
		return reply(200, `{"issues":[],"isLast":true}`), nil
	}
	c4, d4, _ := mk(t, search, jiracloudlite.Options{})
	if _, err := c4.SearchAll(bg, "project = ABC", nil, 0); err != nil || len(d4.calls) != 2 {
		t.Fatalf("search: err = %v calls = %d", err, len(d4.calls))
	}
}

func TestPostRetriedOn429(t *testing.T) {
	h := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(429, "", "Retry-After", "1"), nil
		}
		return reply(204, ""), nil
	}
	c, d, sl := mk(t, h, jiracloudlite.Options{})
	if err := c.DoTransition(bg, "ABC-1", "21", nil); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || len(sl.delays) != 1 || d.calls[0].body != d.calls[1].body {
		t.Fatalf("calls = %+v delays = %v", d.calls, sl.delays)
	}
}

func TestAPIErrorMessages(t *testing.T) {
	body := `{"errorMessages":["bad jql"],"errors":{"jql":"oops","fields":"nope"}}`
	c, _, _ := mk(t, always(400, body), jiracloudlite.Options{})
	_, err := c.Transitions(bg, "ABC-1")
	var apiErr *jiracloudlite.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v", err)
	}
	if apiErr.StatusCode != 400 || len(apiErr.Messages) != 3 || apiErr.Messages[0] != "bad jql" || apiErr.Messages[1] != "fields: nope" || apiErr.Messages[2] != "jql: oops" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "400") || apiErr.Code() != "api_error" {
		t.Fatalf("error text = %v", err)
	}
	if errors.Is(err, jiracloudlite.ErrNotFound) || errors.Is(err, jiracloudlite.ErrRateLimited) {
		t.Fatal("400 must not match ErrNotFound or ErrRateLimited")
	}
}

func TestAPIErrorNotFound(t *testing.T) {
	c, _, _ := mk(t, always(404, `{"errorMessages":["Issue does not exist"],"errors":{}}`), jiracloudlite.Options{})
	_, err := c.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrNotFound)
	if errors.Is(err, jiracloudlite.ErrRateLimited) {
		t.Fatal("404 must not match ErrRateLimited")
	}
}

func TestAPIErrorTruncationAndCaps(t *testing.T) {
	long := strings.Repeat("é", 300)
	c, _, _ := mk(t, always(400, `{"errorMessages":["`+long+`"]}`), jiracloudlite.Options{})
	_, err := c.Transitions(bg, "ABC-1")
	var apiErr *jiracloudlite.APIError
	if !errors.As(err, &apiErr) || len(apiErr.Messages) != 1 || len([]rune(apiErr.Messages[0])) != 200 {
		t.Fatalf("error = %v", err)
	}

	var b strings.Builder
	b.WriteString(`{"errorMessages":[`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"m%d"`, i)
	}
	b.WriteString(`]}`)
	c2, _, _ := mk(t, always(400, b.String()), jiracloudlite.Options{})
	_, err = c2.Transitions(bg, "ABC-1")
	if !errors.As(err, &apiErr) || len(apiErr.Messages) != 20 {
		t.Fatalf("messages = %d, want 20", len(apiErr.Messages))
	}
	if !strings.Contains(err.Error(), "m2") || strings.Contains(err.Error(), "m3") {
		t.Fatalf("Error() should show three messages: %v", err)
	}
}

func TestAPIErrorUnparseableBodies(t *testing.T) {
	for _, body := range []string{"<html>oops</html>", "", `{"errors":{"a":{"b":1}}}`, `{"errorMessages":[""]}`} {
		c, _, _ := mk(t, always(500, body), jiracloudlite.Options{})
		_, err := c.Transitions(bg, "ABC-1")
		var apiErr *jiracloudlite.APIError
		if !errors.As(err, &apiErr) || len(apiErr.Messages) != 0 {
			t.Fatalf("body %q: error = %v", body, err)
		}
		if err.Error() != "jiracloudlite: jira returned HTTP 500" {
			t.Fatalf("Error() = %q", err.Error())
		}
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	auth := jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}
	c, err := jiracloudlite.New(testBase, auth, jiracloudlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{
		auth.String(),
		auth.GoString(),
		fmt.Sprintf("%v", auth),
		fmt.Sprintf("%+v", auth),
		fmt.Sprintf("%#v", auth),
		fmt.Sprintf("%+v", &auth),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%+v", c),
		fmt.Sprintf("%#v", c),
		fmt.Sprintf("%+v", *c),
		c.String(),
		c.GoString(),
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(testCreds))
	for _, out := range outputs {
		if strings.Contains(out, testToken) || strings.Contains(out, b64) {
			t.Errorf("output leaks the secret: %s", out)
		}
	}
	if !strings.Contains(outputs[0], testEmail) || !strings.Contains(outputs[0], "***") {
		t.Errorf("String() = %q", outputs[0])
	}

	c2, _, _ := mk(t, always(401, `{"errorMessages":["Unauthorized"]}`), jiracloudlite.Options{})
	_, err = c2.Transitions(bg, "ABC-1")
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(fmt.Sprintf("%+v", err), testToken) {
		t.Fatalf("error = %v", err)
	}
}

func TestIssueKeyValidation(t *testing.T) {
	c, d, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{})
	longKey := strings.Repeat("A", 300) + "-1"
	bad := []string{"abc-1", "../x", "ABC-01", "ABC-1 ", "ABC-1\n", "", "ABC-1/transitions", "A-1", "ABC-0", "0", "01", longKey}
	for _, key := range bad {
		var v any
		var err error
		_, err = c.Transitions(bg, key)
		expectErr(t, err, jiracloudlite.ErrInvalidIssueKey)
		expectErr(t, c.DoTransition(bg, key, "21", nil), jiracloudlite.ErrInvalidIssueKey)
		expectErr(t, c.TransitionByName(bg, key, "Done"), jiracloudlite.ErrInvalidIssueKey)
		expectErr(t, c.GetProperty(bg, key, "k", &v), jiracloudlite.ErrInvalidIssueKey)
		expectErr(t, c.SetProperty(bg, key, "k", 1), jiracloudlite.ErrInvalidIssueKey)
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
	for _, key := range []string{"ABC-1", "A1_B-22", "10001"} {
		if _, err := c.Transitions(bg, key); err != nil {
			t.Fatalf("key %q rejected: %v", key, err)
		}
		last := d.calls[len(d.calls)-1]
		if last.path != "/rest/api/3/issue/"+key+"/transitions" {
			t.Fatalf("path = %s", last.path)
		}
	}
	var e *jiracloudlite.Error
	_, err := c.Transitions(bg, "bad")
	if !errors.As(err, &e) || e.Code != "invalid_issue_key" {
		t.Fatalf("error = %v", err)
	}
}

func TestPropertyKeyValidation(t *testing.T) {
	c, d, _ := mk(t, always(200, `{"key":"k","value":1}`), jiracloudlite.Options{})
	long := strings.Repeat("é", 256)
	var v any
	for _, key := range []string{long, "", "\xff", strings.Repeat("a", 2000)} {
		expectErr(t, c.GetProperty(bg, "ABC-1", key, &v), jiracloudlite.ErrInvalidPropertyKey)
		expectErr(t, c.SetProperty(bg, "ABC-1", key, 1), jiracloudlite.ErrInvalidPropertyKey)
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
	ok := strings.Repeat("é", 255)
	if err := c.GetProperty(bg, "ABC-1", ok, &v); err != nil || len(d.calls) != 1 {
		t.Fatalf("255-rune key: err = %v calls = %d", err, len(d.calls))
	}
}

func TestPropertyPathEscaping(t *testing.T) {
	c, d, _ := mk(t, always(200, `{"key":"a/b","value":{"n":1}}`), jiracloudlite.Options{})
	var pv propValue
	if err := c.GetProperty(bg, "ABC-1", "a/b", &pv); err != nil {
		t.Fatal(err)
	}
	if pv.N != 1 {
		t.Fatalf("value = %+v", pv)
	}
	if !strings.HasSuffix(d.calls[0].path, "/properties/a%2Fb") {
		t.Fatalf("path = %s", d.calls[0].path)
	}
	if err := c.SetProperty(bg, "ABC-1", "a b?#", pv); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(d.calls[1].path, "/properties/a%20b%3F%23") || d.calls[1].method != "PUT" || d.calls[1].body != `{"n":1}` {
		t.Fatalf("call = %+v", d.calls[1])
	}
}

func TestGetPropertyErrors(t *testing.T) {
	var pv propValue
	c, _, _ := mk(t, always(200, `{"key":"k","value":"text"}`), jiracloudlite.Options{})
	expectErr(t, c.GetProperty(bg, "ABC-1", "k", &pv), jiracloudlite.ErrInvalidResponse)
	expectErr(t, c.GetProperty(bg, "ABC-1", "k", nil), jiracloudlite.ErrInvalidArgument)
	c2, _, _ := mk(t, always(200, `{"key":"k"}`), jiracloudlite.Options{})
	expectErr(t, c2.GetProperty(bg, "ABC-1", "k", &pv), jiracloudlite.ErrInvalidResponse)
	c3, _, _ := mk(t, always(404, `{"errorMessages":["no property"]}`), jiracloudlite.Options{})
	expectErr(t, c3.GetProperty(bg, "ABC-1", "k", &pv), jiracloudlite.ErrNotFound)
}

func TestSetPropertyValueLimit(t *testing.T) {
	c, d, _ := mk(t, always(200, ""), jiracloudlite.Options{})
	atLimit := `"` + strings.Repeat("a", 32_766) + `"`
	if len(atLimit) != 32768 {
		t.Fatalf("fixture length = %d", len(atLimit))
	}
	if err := c.SetProperty(bg, "ABC-1", "k", json.RawMessage(atLimit)); err != nil {
		t.Fatal(err)
	}
	if d.calls[0].body != atLimit {
		t.Fatal("body was altered")
	}
	overLimit := `"` + strings.Repeat("a", 32_767) + `"`
	if len(overLimit) != 32_769 {
		t.Fatalf("fixture length = %d", len(overLimit))
	}
	expectErr(t, c.SetProperty(bg, "ABC-1", "k", json.RawMessage(overLimit)), jiracloudlite.ErrInvalidPropertyValue)
	expectErr(t, c.SetProperty(bg, "ABC-1", "k", make(chan int)), jiracloudlite.ErrInvalidPropertyValue)
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
}

func TestResponseTooLarge(t *testing.T) {
	const limit = 10 << 20
	size := limit + 1
	big := strings.Repeat("a", size)
	var events []event
	onEvent := func(method, tpl string, status, attempt int) {
		events = append(events, event{method, tpl, status, attempt})
	}
	c, _, _ := mk(t, always(200, big), jiracloudlite.Options{OnEvent: onEvent})
	_, err := c.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrResponseTooLarge)
	want := event{"GET", transitionsTemplate, 200, 1}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events = %+v", events)
	}
	c2, _, _ := mk(t, always(500, big), jiracloudlite.Options{})
	_, err = c2.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrResponseTooLarge)

	prefix := `{"transitions":[],"pad":"`
	suffix := `"}`
	pad := limit - len(prefix) - len(suffix)
	exact := prefix + strings.Repeat("a", pad) + suffix
	if len(exact) != limit {
		t.Fatalf("fixture length = %d", len(exact))
	}
	c3, _, _ := mk(t, always(200, exact), jiracloudlite.Options{})
	list, err := c3.Transitions(bg, "ABC-1")
	if err != nil || len(list) != 0 {
		t.Fatalf("exactly 10 MiB must be accepted: list = %v err = %v", list, err)
	}
}

func TestOnEventNeverSeesJQLOrBodies(t *testing.T) {
	var events []event
	onEvent := func(method, tpl string, status, attempt int) {
		events = append(events, event{method, tpl, status, attempt})
	}
	c, _, _ := mk(t, always(200, `{"issues":[{"id":"1","key":"ABC-1"}],"isLast":true}`), jiracloudlite.Options{OnEvent: onEvent})
	if _, err := c.SearchAll(bg, "project = SECRETPROJ", nil, 0); err != nil {
		t.Fatal(err)
	}
	want := event{"POST", "/rest/api/3/search/jql", 200, 1}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events = %+v", events)
	}
	if text := fmt.Sprint(events); strings.Contains(text, "SECRETPROJ") || strings.Contains(text, "ABC-1") {
		t.Fatalf("events leak data: %s", text)
	}
}

func TestOnEventReportsRetriesAndTransportFailures(t *testing.T) {
	var events []event
	onEvent := func(method, tpl string, status, attempt int) {
		events = append(events, event{method, tpl, status, attempt})
	}
	h := func(n int, _ *http.Request) (*http.Response, error) {
		if n == 1 {
			return reply(429, ""), nil
		}
		return reply(200, transitionsJSON), nil
	}
	c, _, _ := mk(t, h, jiracloudlite.Options{OnEvent: onEvent})
	if _, err := c.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
	first := event{"GET", transitionsTemplate, 429, 1}
	second := event{"GET", transitionsTemplate, 200, 2}
	if len(events) != 2 || events[0] != first || events[1] != second {
		t.Fatalf("events = %+v", events)
	}

	events = nil
	dial := func(int, *http.Request) (*http.Response, error) { return nil, errBoom }
	c2, d2, _ := mk(t, dial, jiracloudlite.Options{OnEvent: onEvent})
	_, err := c2.Transitions(bg, "ABC-1")
	expectErr(t, err, errBoom)
	if len(d2.calls) != 1 || len(events) != 1 || events[0].status != 0 {
		t.Fatalf("calls = %d events = %+v", len(d2.calls), events)
	}
}

func TestUnusualResponses(t *testing.T) {
	nilResp := func(int, *http.Request) (*http.Response, error) { return nil, nil }
	c, _, _ := mk(t, nilResp, jiracloudlite.Options{})
	if _, err := c.Transitions(bg, "ABC-1"); err == nil {
		t.Fatal("nil response must be an error")
	}
	badBody := func(int, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
	}
	c2, _, _ := mk(t, badBody, jiracloudlite.Options{})
	_, err := c2.Transitions(bg, "ABC-1")
	expectErr(t, err, errBoom)
	c3, _, _ := mk(t, always(200, "not json"), jiracloudlite.Options{})
	_, err = c3.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrInvalidResponse)
	c4, _, _ := mk(t, always(200, ""), jiracloudlite.Options{})
	_, err = c4.Transitions(bg, "ABC-1")
	expectErr(t, err, jiracloudlite.ErrInvalidResponse)
}

func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	defer origin.Close()
	c, err := jiracloudlite.New(origin.URL, jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}, jiracloudlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Transitions(bg, "ABC-1")
	var apiErr *jiracloudlite.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want *APIError with status 302", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect target was requested %d times", hits.Load())
	}
}

func TestRequestShape(t *testing.T) {
	var host string
	h := func(_ int, r *http.Request) (*http.Response, error) {
		host = r.URL.Host
		return reply(200, transitionsJSON), nil
	}
	d := &fakeDoer{handler: h}
	auth := jiracloudlite.BasicAuth{Email: testEmail, APIToken: testToken}
	c, err := jiracloudlite.New("https://example.com/jira/", auth, jiracloudlite.Options{Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if host != "example.com" || d.calls[0].path != "/jira/rest/api/3/issue/ABC-1/transitions" {
		t.Fatalf("host = %s path = %s", host, d.calls[0].path)
	}
	if got := d.calls[0].header.Get("User-Agent"); got != "jiracloudlite/1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if d.calls[0].header.Get("Content-Type") != "" || d.calls[0].header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", d.calls[0].header)
	}
	c2, _, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{UserAgent: "my-bot/2"})
	c2d := c2
	if _, err := c2d.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
}

func TestUserAgentCustom(t *testing.T) {
	c, d, _ := mk(t, always(200, transitionsJSON), jiracloudlite.Options{UserAgent: "my-bot/2"})
	if _, err := c.Transitions(bg, "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if got := d.calls[0].header.Get("User-Agent"); got != "my-bot/2" {
		t.Fatalf("User-Agent = %q", got)
	}
}

func TestSentinelCodes(t *testing.T) {
	codes := map[*jiracloudlite.Error]string{
		jiracloudlite.ErrInvalidBaseURL:       "invalid_base_url",
		jiracloudlite.ErrInvalidIssueKey:      "invalid_issue_key",
		jiracloudlite.ErrInvalidPropertyKey:   "invalid_property_key",
		jiracloudlite.ErrInvalidPropertyValue: "invalid_property_value",
		jiracloudlite.ErrNotFound:             "not_found",
		jiracloudlite.ErrTransitionNotFound:   "transition_not_found",
		jiracloudlite.ErrRateLimited:          "rate_limited",
		jiracloudlite.ErrPaginationLoop:       "pagination_loop",
		jiracloudlite.ErrResponseTooLarge:     "response_too_large",
		jiracloudlite.ErrInvalidArgument:      "invalid_argument",
		jiracloudlite.ErrInvalidResponse:      "invalid_response",
	}
	for e, code := range codes {
		if e.Code != code || !strings.HasPrefix(e.Error(), "jiracloudlite: ") {
			t.Errorf("sentinel %q: code = %q message = %q", code, e.Code, e.Error())
		}
	}
}

func ExampleBasicAuth() {
	auth := jiracloudlite.BasicAuth{Email: "bot@example.invalid", APIToken: "do-not-print"}
	fmt.Println(auth)
	// Output: BasicAuth{Email: bot@example.invalid, APIToken: ***}
}
