package snapnedit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient wires a client to a test server with retries fast enough not to
// slow the suite down.
func testClient(t *testing.T, handler http.Handler, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base := []Option{
		WithBaseURL(srv.URL),
		WithRetry(RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}),
	}
	return NewClient("sk_live_test", append(base, opts...)...)
}

// writeJSON is a tiny handler helper.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeAPIError writes the API's uniform error envelope.
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("sk_live_x")
	if c.BaseURL() != DefaultBaseURL {
		t.Fatalf("base url = %q, want %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.Destinations == nil || c.Embed == nil || c.Designs == nil {
		t.Fatal("services should be wired on construction")
	}
	if c.retry != DefaultRetry {
		t.Fatalf("retry = %+v, want %+v", c.retry, DefaultRetry)
	}
}

func TestWithBaseURLTrimsTrailingSlash(t *testing.T) {
	c := NewClient("k", WithBaseURL("https://example.test/api/"))
	if c.BaseURL() != "https://example.test/api" {
		t.Fatalf("base url = %q", c.BaseURL())
	}
}

func TestAuthorizationHeaderAndUserAgent(t *testing.T) {
	var gotAuth, gotUA string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		writeJSON(w, 200, []OperationMetadata{})
	}))
	if _, err := c.ListOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk_live_test" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "snapnedit-go/") {
		t.Fatalf("user-agent = %q", gotUA)
	}
}

func TestPresignedPathIsResolvedAgainstABaseWithAPrefix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("bytes"))
	}))
	t.Cleanup(srv.Close)

	c := NewClient("k", WithBaseURL(srv.URL+"/api"))
	body, mime, err := c.Download(context.Background(), SignedURL{URL: "/_local/results/abc.png?exp=1&sig=2"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/_local/results/abc.png?exp=1&sig=2" {
		t.Fatalf("path = %q", gotPath)
	}
	if string(body) != "bytes" || mime != "image/png" {
		t.Fatalf("body=%q mime=%q", body, mime)
	}
}

func TestErrorEnvelopeBecomesTypedError(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 402, "payment_required", "out of credits")
	}))
	_, err := c.GetJob(context.Background(), "job-1")
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("errors.Is(ErrPaymentRequired) = false for %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*Error) = false for %v", err)
	}
	if apiErr.Status != 402 || apiErr.Code != CodePaymentRequired || apiErr.Message != "out of credits" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a payment_required error must not match ErrNotFound")
	}
}

func TestUnknownErrorCodeDegradesToInternal(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 418, "teapot_engaged", "unknown to this sdk")
	}))
	_, err := c.GetJob(context.Background(), "job-1")
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if apiErr.Code != CodeInternal {
		t.Fatalf("code = %q, want internal", apiErr.Code)
	}
	if apiErr.Message != "unknown to this sdk" {
		t.Fatalf("message = %q — the server's message should survive", apiErr.Message)
	}
}

func TestNonEnvelopeErrorBodyStillTyped(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}), WithRetry(RetryConfig{}))
	_, err := c.GetJob(context.Background(), "job-1")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInternal || apiErr.Status != 503 {
		t.Fatalf("err = %v", err)
	}
}

func TestRetriesIdempotentRequestsOn429And5xx(t *testing.T) {
	var calls int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			writeAPIError(w, 429, "rate_limited", "slow down")
		case 2:
			writeAPIError(w, 500, "internal", "boom")
		default:
			writeJSON(w, 200, map[string]any{"state": "succeeded", "outputAssetId": "a", "input": map[string]string{"kind": "asset"}})
		}
	}))
	job, err := c.GetJob(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobSucceeded {
		t.Fatalf("state = %q", job.State)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestRetriesGiveUpAfterMaxAttempts(t *testing.T) {
	var calls int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeAPIError(w, 503, "internal", "still down")
	}))
	if _, err := c.GetJob(context.Background(), "job-1"); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3 (MaxAttempts)", got)
	}
}

func TestPostIsNeverRetried(t *testing.T) {
	var calls int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeAPIError(w, 500, "internal", "boom")
	}))
	_, err := c.CreateJob(context.Background(), OpRemoveBackground, FromAsset("asset-1"), nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d — a POST that creates a job must not be replayed", got)
	}
}

func TestRetryAfterHeaderIsHonoured(t *testing.T) {
	c := NewClient("k", WithRetry(RetryConfig{MaxAttempts: 2, InitialBackoff: time.Hour, MaxBackoff: 50 * time.Millisecond}))
	delay := c.backoff(1, &rawResponse{headers: http.Header{"Retry-After": []string{"0"}}})
	if delay != 0 {
		t.Fatalf("Retry-After: 0 should win over the computed backoff, got %s", delay)
	}
	capped := c.backoff(1, &rawResponse{headers: http.Header{"Retry-After": []string{"3600"}}})
	if capped != 50*time.Millisecond {
		t.Fatalf("Retry-After should still be capped by MaxBackoff, got %s", capped)
	}
}

func TestAnonymousClientRefusesAccountRoutesLocally(t *testing.T) {
	c := NewClient("")
	if _, err := c.Upload(context.Background(), FromBytes([]byte("x"), "image/png")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if _, err := c.Destinations.List(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	var calls int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.GetJob(ctx, "job-1"); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d — a canceled context must not be retried", got)
	}
}
