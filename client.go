package snapnedit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public snapnedit API. The website proxies /api/* to
// the API service, so this origin and the service's own routes are the same
// endpoints. Override it with [WithBaseURL] for a self-hosted deployment or
// for local development (http://localhost:8787).
const DefaultBaseURL = "https://snapnedit.com/api"

// Version is this SDK's version, sent in the User-Agent header.
const Version = "0.1.1"

// maskParamKey is the params key mask-guided operations read their mask from.
// It is pipeline plumbing rather than part of any operation's schema.
const maskParamKey = "maskAssetId"

// Client is a snapnedit API client. It is safe for concurrent use by multiple
// goroutines. Create one with [NewClient] and keep it for the process
// lifetime.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
	retry      RetryConfig
	userAgent  string

	// Destinations manages the account's saved storage destinations.
	Destinations *DestinationsService
	// Embed mints short-lived tokens for the embedded editor.
	Embed *EmbedService
	// Designs composes and renders multi-layer designs server-side.
	Designs *DesignsService
}

// RetryConfig controls automatic retries. Only idempotent requests (GET, HEAD,
// PUT, DELETE) are ever retried — a POST that creates a job or a destination
// is not, because a response this client never saw may still have taken
// effect.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts, the first one included. Any
	// value below 2 disables retries.
	MaxAttempts int
	// InitialBackoff is the delay before the second attempt. It doubles each
	// further attempt, with jitter, up to MaxBackoff. A Retry-After response
	// header always wins over the computed delay.
	InitialBackoff time.Duration
	// MaxBackoff caps the delay between attempts.
	MaxBackoff time.Duration
}

// DefaultRetry is the retry policy a client uses unless [WithRetry] says
// otherwise: three attempts, 250ms of backoff doubling to a 5s ceiling.
var DefaultRetry = RetryConfig{
	MaxAttempts:    3,
	InitialBackoff: 250 * time.Millisecond,
	MaxBackoff:     5 * time.Second,
}

// Option configures a [Client] at construction time.
type Option func(*Client)

// WithBaseURL points the client at a different deployment — a self-hosted
// instance, or http://localhost:8787 in local development. A trailing slash is
// harmless.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if parsed, err := url.Parse(strings.TrimRight(baseURL, "/")); err == nil {
			c.baseURL = parsed
		}
	}
}

// WithHTTPClient supplies the [http.Client] to make requests with — for a
// custom transport, a proxy, or a global timeout. Note that a Timeout set on
// the http.Client bounds each individual request, not [Client.Run]'s whole
// upload-poll-download flow; bound that with the context or
// [RunOptions.Timeout].
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithRetry replaces the retry policy. Pass a zero [RetryConfig] to disable
// retries entirely.
func WithRetry(cfg RetryConfig) Option {
	return func(c *Client) { c.retry = cfg }
}

// WithUserAgent appends a product token to the User-Agent this client sends.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua + " " + c.userAgent
		}
	}
}

// NewClient builds a client that authenticates with apiKey — a secret
// sk_live_… API key, or an embed token, sent as `Authorization: Bearer`.
//
// An empty apiKey produces an anonymous client, which can still reach the
// unauthenticated routes ([Client.ListOperations],
// [EmbedService.CreateSession], [DesignsService.Render]).
func NewClient(apiKey string, opts ...Option) *Client {
	base, _ := url.Parse(DefaultBaseURL)
	c := &Client{
		baseURL:    base,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		retry:      DefaultRetry,
		userAgent:  "snapnedit-go/" + Version,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.Destinations = &DestinationsService{client: c}
	c.Embed = &EmbedService{client: c}
	c.Designs = &DesignsService{client: c}
	return c
}

// BaseURL reports the origin this client talks to.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// ---------------------------------------------------------------------------
// Request plumbing
// ---------------------------------------------------------------------------

// request is one HTTP call, described declaratively so it can be replayed on a
// retry.
type request struct {
	method string
	// path is relative to the client's base URL, e.g. "/jobs". Exactly one of
	// path or rawURL is set.
	path string
	// rawURL is an absolute or base-relative URL used verbatim — for presigned
	// upload and download URLs, which the API returns as same-origin paths.
	rawURL string
	// body is the encoded request body, replayed as-is on every attempt.
	body []byte
	// contentType is the body's media type, omitted when body is nil.
	contentType string
	// auth adds the Authorization header when the client has a key.
	auth bool
	// headers are extra headers, sent verbatim (a presigned PUT's signature
	// covers them).
	headers map[string]string
	// acceptJSON asks for a JSON response.
	acceptJSON bool
}

// idempotent reports whether this request may be retried. A POST may not: the
// server may have acted on an attempt whose response never arrived.
func (r request) idempotent() bool {
	switch r.method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

// resolve turns a request's path or rawURL into an absolute URL against the
// client's base.
func (c *Client) resolve(r request) (string, error) {
	target := r.rawURL
	if target == "" {
		target = r.path
	}
	ref, err := url.Parse(target)
	if err != nil {
		return "", newError(CodeInternal, 0, err, "invalid url %q", target)
	}
	if ref.IsAbs() {
		return ref.String(), nil
	}
	// The API mints presigned URLs as same-origin paths (/_local/…?exp&sig);
	// resolve them against the base so they work against any deployment.
	base := *c.baseURL
	if !strings.HasPrefix(ref.Path, "/") {
		return base.ResolveReference(ref).String(), nil
	}
	joined := *c.baseURL
	joined.Path = strings.TrimRight(c.baseURL.Path, "/") + ref.Path
	joined.RawQuery = ref.RawQuery
	joined.Fragment = ref.Fragment
	return joined.String(), nil
}

// rawResponse is a fully-read HTTP response.
type rawResponse struct {
	status  int
	headers http.Header
	body    []byte
}

// contentType reports the response's media type, parameters included.
func (r *rawResponse) contentType() string { return r.headers.Get("Content-Type") }

// send performs one request, retrying per the client's [RetryConfig], and
// returns the fully-read response. A non-2xx status is returned as an [*Error]
// parsed from the API's `{ error: { code, message } }` envelope.
func (c *Client) send(ctx context.Context, r request) (*rawResponse, error) {
	target, err := c.resolve(r)
	if err != nil {
		return nil, err
	}

	attempts := c.retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	if !r.idempotent() {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		resp, err := c.attempt(ctx, r, target)
		if err == nil && resp.status < 400 {
			return resp, nil
		}

		var apiErr error
		var retryable bool
		if err != nil {
			apiErr = err
			// A context that is done is the caller's decision, never a retry.
			retryable = ctx.Err() == nil
		} else {
			apiErr = parseErrorResponse(resp, target)
			retryable = resp.status == http.StatusTooManyRequests || resp.status >= 500
		}
		lastErr = apiErr

		if attempt >= attempts || !retryable {
			return nil, lastErr
		}
		delay := c.backoff(attempt, resp)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, newError(CodeInternal, 0, ctx.Err(), "request to %s canceled while backing off", target)
		case <-timer.C:
		}
	}
}

// attempt performs exactly one HTTP round trip and reads the whole body.
func (c *Client) attempt(ctx context.Context, r request, target string) (*rawResponse, error) {
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return nil, newError(CodeInternal, 0, err, "could not build request to %s", target)
	}
	if r.body != nil && r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if r.acceptJSON {
		req.Header.Set("Accept", "application/json")
	}
	if r.auth && c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("User-Agent", c.userAgent)
	for name, value := range r.headers {
		req.Header.Set(name, value)
	}
	// http.NewRequest cannot know the length of a bytes.Reader wrapped in an
	// io.Reader, and some presigned-PUT schemes insist on Content-Length.
	if r.body != nil {
		req.ContentLength = int64(len(r.body))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, newError(CodeInternal, 0, err, "network error calling %s", target)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, newError(CodeInternal, resp.StatusCode, err, "could not read response from %s", target)
	}
	return &rawResponse{status: resp.StatusCode, headers: resp.Header, body: payload}, nil
}

// backoff computes how long to wait before the next attempt, preferring the
// server's own Retry-After header when it sent one.
func (c *Client) backoff(attempt int, resp *rawResponse) time.Duration {
	if resp != nil {
		if after, ok := retryAfter(resp.headers.Get("Retry-After")); ok {
			if c.retry.MaxBackoff > 0 && after > c.retry.MaxBackoff {
				return c.retry.MaxBackoff
			}
			return after
		}
	}
	base := c.retry.InitialBackoff
	if base <= 0 {
		base = DefaultRetry.InitialBackoff
	}
	delay := base << (attempt - 1)
	if max := c.retry.MaxBackoff; max > 0 && delay > max {
		delay = max
	}
	// Full jitter over the lower half of the window keeps concurrent clients
	// from retrying in lockstep without ever waiting less than half the target.
	jitter := time.Duration(rand.Int63n(int64(delay/2) + 1)) //nolint:gosec // jitter, not cryptography
	return delay/2 + jitter
}

// retryAfter parses a Retry-After header in either of its forms — delta
// seconds, or an HTTP date. The bool reports whether the header was present and
// usable; a valid "0" is a real instruction to retry immediately, not an
// absent header.
func retryAfter(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

// errorEnvelope is the uniform failure body every API route sends.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseErrorResponse turns a non-2xx response into an [*Error], falling back to
// a generic [CodeInternal] error when the body is not the documented envelope.
func parseErrorResponse(resp *rawResponse, target string) *Error {
	var envelope errorEnvelope
	if err := json.Unmarshal(resp.body, &envelope); err == nil && envelope.Error.Code != "" {
		return &Error{
			Code:    asErrorCode(envelope.Error.Code),
			Message: envelope.Error.Message,
			Status:  resp.status,
		}
	}
	return newError(CodeInternal, resp.status, nil, "request to %s failed with status %d", target, resp.status)
}

// doJSON sends r and decodes a JSON response body into out. Pass a nil out for
// a route that answers 204 No Content.
func (c *Client) doJSON(ctx context.Context, r request, out any) error {
	r.acceptJSON = true
	resp, err := c.send(ctx, r)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(resp.body)) == 0 {
		return newError(CodeInternal, resp.status, nil, "%s %s returned an empty body", r.method, r.path)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return newError(CodeInternal, resp.status, err, "%s %s returned a body this client could not parse", r.method, r.path)
	}
	return nil
}

// jsonBody encodes v for a request body, reporting a caller-side encoding
// failure as an [*Error] rather than a bare error.
func jsonBody(v any) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, newError(CodeInvalidInput, 0, err, "could not encode request body")
	}
	return encoded, nil
}

// requireKey guards the routes that are meaningless without a credential, so
// an unconfigured client fails locally instead of provoking a 401.
func (c *Client) requireKey(what string) error {
	if c.apiKey == "" {
		return newError(CodeUnauthorized, 0, nil, "%s requires an API key — build the client with NewClient(apiKey)", what)
	}
	return nil
}
