// Package mixpost is a thin client over the Mixpost HTTP API.
//
// Responses are handed back as raw JSON so that fields the CLI does not know
// about still reach the user untouched; only the few payloads the CLI has to
// act on (uploads, pagination links) are decoded into types.
package mixpost

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one Mixpost instance.
type Client struct {
	// BaseURL is the instance URL including the core path, without /api.
	BaseURL string
	Key     string
	// Workspace is the uuid injected into workspace-scoped paths. It may also
	// be a workspace name or numeric id, which is resolved to a uuid once, the
	// first time a workspace-scoped request needs it.
	Workspace string

	HTTP      *http.Client
	Retries   int
	UserAgent string

	// Debug, when set, writes a request/response trace to DebugOut.
	Debug    bool
	DebugOut io.Writer

	// DryRun stops the client before the request leaves the process; Do
	// returns the prepared request as a Response with status 0. Read-only
	// lookups that resolve names to ids still run.
	DryRun bool

	workspaceOnce sync.Once
	workspaceUUID string
	workspaceErr  error
}

// Options configures a Client.
type Options struct {
	BaseURL   string
	Key       string
	Workspace string
	Timeout   time.Duration
	Retries   int
	Insecure  bool
	UserAgent string
	Debug     bool
	DebugOut  io.Writer
	DryRun    bool
}

// New builds a Client from Options.
func New(opts Options) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.Insecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "mixpost-cli"
	}
	return &Client{
		BaseURL:   strings.TrimSuffix(opts.BaseURL, "/"),
		Key:       opts.Key,
		Workspace: opts.Workspace,
		HTTP:      &http.Client{Timeout: timeout, Transport: transport},
		Retries:   opts.Retries,
		UserAgent: ua,
		Debug:     opts.Debug,
		DebugOut:  opts.DebugOut,
		DryRun:    opts.DryRun,
	}
}

// Response is a successful API response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte

	// Request describes what was sent; it is the only field populated in a
	// dry run.
	Request RequestInfo
}

// RequestInfo is a record of an outgoing request, used for --dry-run and debug.
type RequestInfo struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        json.RawMessage   `json:"body,omitempty"`
	Description string            `json:"description,omitempty"`
}

// Request is a single API call.
type Request struct {
	Method string
	// Path is relative to /api, e.g. "posts" for a workspace-scoped call or
	// "workspaces" for an instance-level one.
	Path string
	// Instance forces an instance-level path, one that is never prefixed with
	// a workspace uuid. Paths IsInstancePath recognises need no flag.
	Instance bool
	Query    url.Values
	// Body is marshalled to JSON. Use RawBody to send bytes verbatim.
	Body    any
	RawBody []byte
	// AbsoluteURL bypasses path building; used to follow pagination links.
	AbsoluteURL string
	Headers     map[string]string

	// BodyFactory supplies a streaming body. It is called once per attempt so
	// that retries send the payload again from the start.
	BodyFactory func() (io.Reader, string, error)
}

// ErrNoWorkspace is returned when a workspace-scoped call has no workspace set.
var ErrNoWorkspace = errors.New("no workspace selected: run 'mixpost workspace list' to find one, then pass -w, or run 'mixpost config set workspace <uuid>'")

// ErrNoKey is returned when the client has no personal key.
var ErrNoKey = errors.New("no personal key: pass --personal-key, set MIXPOST_PERSONAL_KEY, or run 'mixpost auth login'")

// ErrWorkspaceNotFound is returned when a workspace name matches nothing, or
// more than one thing.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// APIBase returns the URL prefix shared by every endpoint.
func (c *Client) APIBase() string { return c.BaseURL + "/api" }

// resolveURL builds the absolute URL for a request.
func (c *Client) resolveURL(r Request) (string, error) {
	if r.AbsoluteURL != "" {
		return appendQuery(r.AbsoluteURL, r.Query)
	}
	if c.BaseURL == "" {
		return "", fmt.Errorf("no instance URL: pass --url, set %s, or run 'mixpost auth login'", "MIXPOST_URL")
	}

	path := strings.TrimPrefix(r.Path, "/")
	if r.Instance || IsInstancePath(path) {
		return appendQuery(c.APIBase()+"/"+path, r.Query)
	}

	if c.Workspace == "" {
		return "", ErrNoWorkspace
	}
	full := c.APIBase() + "/" + c.workspaceID()
	if path != "" {
		full += "/" + path
	}
	return appendQuery(full, r.Query)
}

// workspaceID is the uuid to address, which is the configured value unless a
// name was given and has since been resolved.
func (c *Client) workspaceID() string {
	if c.workspaceUUID != "" {
		return c.workspaceUUID
	}
	return c.Workspace
}

// ensureWorkspace turns a workspace name into its uuid, once, the first time a
// workspace-scoped request needs one. A uuid is used exactly as given, so the
// ordinary case costs no extra request. sync.Once orders the write against
// every later read, which matters because chunk uploads call Do concurrently.
func (c *Client) ensureWorkspace(ctx context.Context, r Request) error {
	if r.AbsoluteURL != "" || r.Instance || IsInstancePath(r.Path) {
		return nil
	}
	if c.Workspace == "" || looksLikeUUID(c.Workspace) {
		return nil
	}

	c.workspaceOnce.Do(func() {
		c.workspaceUUID, c.workspaceErr = c.lookupWorkspace(ctx, c.Workspace)
	})
	return c.workspaceErr
}

func (c *Client) lookupWorkspace(ctx context.Context, spec string) (string, error) {
	workspaces, err := c.Workspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("look up the workspace named %q: %w", spec, err)
	}

	var matches []Workspace
	for _, workspace := range workspaces {
		if workspace.UUID == spec ||
			strings.EqualFold(workspace.Name, spec) ||
			strconv.Itoa(workspace.ID) == spec {
			matches = append(matches, workspace)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].UUID, nil
	case 0:
		return "", fmt.Errorf("%w: nothing matches %q; available: %s", ErrWorkspaceNotFound, spec, describeWorkspaces(workspaces))
	default:
		return "", fmt.Errorf("%w: %q matches %d workspaces (%s); use a uuid", ErrWorkspaceNotFound, spec, len(matches), describeWorkspaces(matches))
	}
}

func describeWorkspaces(workspaces []Workspace) string {
	if len(workspaces) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(workspaces))
	for _, workspace := range workspaces {
		parts = append(parts, workspace.Name+"="+workspace.UUID)
	}
	return strings.Join(parts, ", ")
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func looksLikeUUID(value string) bool { return uuidPattern.MatchString(value) }

// LooksLikeUUID reports whether a value has the shape of a uuid, which is how
// commands tell a uuid apart from a name they have to look up.
func LooksLikeUUID(value string) bool { return looksLikeUUID(value) }

// instancePaths are the API paths that sit outside any workspace. "panel" is
// the Mixpost Enterprise admin surface, which this CLI does not wrap but still
// addresses correctly when someone reaches for it through `mixpost raw`.
var instancePaths = []string{"ping", "workspaces", "panel"}

// IsInstancePath reports whether a path is instance-level, and so must not be
// prefixed with a workspace uuid.
func IsInstancePath(path string) bool {
	path = strings.TrimPrefix(path, "/")
	for _, prefix := range instancePaths {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func appendQuery(raw string, q url.Values) (string, error) {
	if len(q) == 0 {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	existing := u.Query()
	for k, vs := range q {
		for _, v := range vs {
			existing.Add(k, v)
		}
	}
	u.RawQuery = existing.Encode()
	return u.String(), nil
}

// Do performs a request, retrying transient failures.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	if c.Key == "" {
		return nil, ErrNoKey
	}
	if err := c.ensureWorkspace(ctx, r); err != nil {
		return nil, err
	}
	target, err := c.resolveURL(r)
	if err != nil {
		return nil, err
	}

	bodyFactory := r.BodyFactory
	contentType := "application/json"
	if bodyFactory == nil {
		var payload []byte
		switch {
		case r.RawBody != nil:
			payload = r.RawBody
		case r.Body != nil:
			payload, err = json.Marshal(r.Body)
			if err != nil {
				return nil, fmt.Errorf("encode request body: %w", err)
			}
		}
		if payload != nil {
			bodyFactory = func() (io.Reader, string, error) {
				return bytes.NewReader(payload), "application/json", nil
			}
		}
	}

	info := RequestInfo{Method: r.Method, URL: target, Headers: map[string]string{}}
	if r.Body != nil || r.RawBody != nil {
		if raw, err := previewBody(r); err == nil {
			info.Body = raw
		}
	}
	if r.BodyFactory != nil {
		info.Description = "streaming body (multipart)"
	}

	if c.DryRun {
		info.Headers["Authorization"] = "Bearer " + redact(c.Key)
		info.Headers["Accept"] = "application/json"
		if bodyFactory != nil && r.BodyFactory == nil {
			info.Headers["Content-Type"] = contentType
		}
		return &Response{Status: 0, Request: info}, nil
	}

	attempts := c.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			delay := backoff(attempt, lastErr)
			c.tracef("retry %d/%d in %s\n", attempt, attempts-1, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.attempt(ctx, r, target, bodyFactory)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return nil, err
		}
		if attempt == attempts-1 || !retryable(r.Method, err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, r Request, target string, bodyFactory func() (io.Reader, string, error)) (*Response, error) {
	var body io.Reader
	contentType := ""
	if bodyFactory != nil {
		var err error
		body, contentType, err = bodyFactory()
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("User-Agent", c.UserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	c.tracef("> %s %s\n", req.Method, req.URL.String())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &TransportError{Method: r.Method, URL: target, Err: err}
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &TransportError{Method: r.Method, URL: target, Err: err}
	}

	c.tracef("< %s (%d bytes)\n", resp.Status, len(payload))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := parseAPIError(resp.StatusCode, r.Method, target, payload)
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			apiErr.retryAfter = parseRetryAfter(ra)
		}
		return nil, apiErr
	}

	return &Response{
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Body:    payload,
		Request: RequestInfo{Method: r.Method, URL: target},
	}, nil
}

// TransportError is a failure that happened before a response was received.
type TransportError struct {
	Method string
	URL    string
	Err    error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Method, e.URL, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// retryAfter is honoured for 429 and 503 responses.
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

func parseRetryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// retryable decides whether a failed attempt is worth repeating. Requests that
// create resources are only retried when the server states it did not process
// them (429), so a flaky connection cannot publish a post twice.
func retryable(method string, err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusTooManyRequests {
			return true
		}
		if apiErr.Status >= 500 {
			return idempotent(method)
		}
		return false
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return idempotent(method)
	}
	return false
}

func idempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func backoff(attempt int, lastErr error) time.Duration {
	var apiErr *APIError
	if errors.As(lastErr, &apiErr) && apiErr.retryAfter > 0 {
		return min(apiErr.retryAfter, 60*time.Second)
	}
	base := 500 * time.Millisecond << (attempt - 1)
	base = min(base, 10*time.Second)
	jitter := time.Duration(rand.Int63n(int64(base / 4)))
	return base + jitter
}

func (c *Client) tracef(format string, args ...any) {
	if !c.Debug || c.DebugOut == nil {
		return
	}
	fmt.Fprintf(c.DebugOut, format, args...)
}

func previewBody(r Request) (json.RawMessage, error) {
	if r.RawBody != nil {
		if json.Valid(r.RawBody) {
			return json.RawMessage(r.RawBody), nil
		}
		return json.Marshal(string(r.RawBody))
	}
	return json.Marshal(r.Body)
}

func redact(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// Redact exposes token masking for the auth commands.
func Redact(token string) string { return redact(token) }
