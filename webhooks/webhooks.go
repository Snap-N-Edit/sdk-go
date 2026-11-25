// Package webhooks verifies and parses snapnedit's outbound webhook
// deliveries.
//
// A snapnedit account can register endpoint URLs that receive a signed HTTP
// POST every time one of its jobs finishes. Authenticate every delivery before
// trusting it:
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		payload, err := io.ReadAll(r.Body)
//		if err != nil {
//			http.Error(w, "unreadable", http.StatusBadRequest)
//			return
//		}
//		event, err := webhooks.ConstructEvent(payload, r.Header.Get(webhooks.SignatureHeader), secret,
//			webhooks.WithTolerance(5*time.Minute))
//		if err != nil {
//			http.Error(w, "bad signature", http.StatusBadRequest)
//			return
//		}
//		log.Println(event.Type, event.Data.JobID)
//		w.WriteHeader(http.StatusOK)
//	}
//
// The payload MUST be the raw bytes you received, verified before any JSON
// parse — a re-serialized body will not match the signature.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Headers snapnedit sets on every webhook delivery.
const (
	// SignatureHeader carries `t=<unix seconds>,v1=<hex>`.
	SignatureHeader = "X-Snapnedit-Signature"
	// EventHeader repeats the event type.
	EventHeader = "X-Snapnedit-Event"
	// DeliveryHeader repeats the delivery id.
	DeliveryHeader = "X-Snapnedit-Delivery"
)

// EventType is the kind of event a delivery carries. Treat an unrecognized
// type as a forward-compatible no-op rather than an error: new types can be
// added without a breaking change.
type EventType string

// The event types snapnedit sends today.
const (
	EventJobSucceeded EventType = "job.succeeded"
	EventJobFailed    EventType = "job.failed"
	// EventSessionEnded fires once an embed session's last token has expired.
	EventSessionEnded EventType = "session.ended"
)

// Verification failures. Every one of them means "reject the delivery"; they
// are distinguished only so a receiver can log precisely.
var (
	// ErrInvalidHeader means the signature header was missing, malformed, or
	// carried no t= / no v1=.
	ErrInvalidHeader = errors.New("snapnedit/webhooks: malformed signature header")
	// ErrSignatureMismatch means no v1 in the header matched the computed MAC.
	ErrSignatureMismatch = errors.New("snapnedit/webhooks: signature does not match")
	// ErrTimestampOutOfTolerance means the signed timestamp was outside the
	// freshness window a caller asked for with [WithTolerance].
	ErrTimestampOutOfTolerance = errors.New("snapnedit/webhooks: signature timestamp is outside the tolerance window")
	// ErrEmptySecret means no signing secret was supplied.
	ErrEmptySecret = errors.New("snapnedit/webhooks: signing secret is empty")
)

// JobEnvelope is the bring-your-own-storage envelope every job event carries:
// which kind of input the job had, whether it had a delivery destination, and
// how the delivery went. Like GET /jobs/{id}, it never carries the input URL,
// the destination URL or its headers — those are bearer credentials for the
// caller's own bucket.
type JobEnvelope struct {
	// Input says whether the job read an uploaded asset or an external URL.
	Input struct {
		// Kind is "asset" or "url".
		Kind string `json:"kind"`
	} `json:"input"`
	// Destination summarizes the delivery target, or nil.
	Destination *struct {
		// Type is "presigned-put" or "saved".
		Type string `json:"type"`
		// ID names a saved destination.
		ID string `json:"id,omitempty"`
		// Name labels a saved destination.
		Name string `json:"name,omitempty"`
	} `json:"destination"`
	// Delivery records the outcome, or nil when there was no destination.
	Delivery *struct {
		// Status is "pending", "delivered" or "failed".
		Status string `json:"status"`
		// Attempts is how many PUTs were made.
		Attempts int `json:"attempts"`
		// DeliveredAt is an RFC 3339 timestamp.
		DeliveredAt string `json:"deliveredAt,omitempty"`
		// StatusCode is the bucket's HTTP status.
		StatusCode int `json:"statusCode,omitempty"`
		// Error explains a failed delivery.
		Error string `json:"error,omitempty"`
		// Key is the object key written, for a saved destination.
		Key string `json:"key,omitempty"`
		// Bucket is the bucket written to, for a saved destination.
		Bucket string `json:"bucket,omitempty"`
	} `json:"delivery"`
	// Test marks a job created with an sk_test_ key (placeholder result, no
	// charge). False when an older API omits it.
	Test bool `json:"test"`
}

// EventData is the `data` object inside a delivery. Which fields are populated
// depends on Status: a succeeded job carries OutputAssetID (and often
// Download), a failed one carries ErrorCode and Message, and a
// [EventSessionEnded] delivery (Status "ended") carries only the session
// fields.
//
// A succeeded job with Delivery.Status "failed" is a real combination and not a
// contradiction: the job ran fine, only the PUT into your bucket did not — the
// result is still at Download.
type EventData struct {
	JobEnvelope
	// JobID identifies the job.
	JobID string `json:"jobId"`
	// Operation is the operation id, as an opaque string.
	Operation string `json:"operation"`
	// Status is "succeeded" or "failed".
	Status string `json:"status"`
	// OutputAssetID names the result asset, on a succeeded job.
	OutputAssetID string `json:"outputAssetId,omitempty"`
	// Download is a presigned URL for the result, when there is one.
	Download string `json:"download,omitempty"`
	// ErrorCode says why a failed job failed.
	ErrorCode string `json:"errorCode,omitempty"`
	// Message is the human explanation for a failed job.
	Message string `json:"message,omitempty"`

	// The session.ended fields. A null in the payload reads as "".

	// SessionID identifies the embed session.
	SessionID string `json:"sessionId,omitempty"`
	// APIKeyID is the publishable or secret key the session was minted with.
	APIKeyID string `json:"apiKeyId,omitempty"`
	// Origin is the host surface the editor was embedded in.
	Origin string `json:"origin,omitempty"`
	// EndUserID is the host's own id for the end user, when it named one.
	EndUserID string `json:"endUserId,omitempty"`
	// StartedAt and EndedAt are RFC 3339 timestamps.
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	// Refreshes is how many times the session's token was re-minted.
	Refreshes int `json:"refreshes,omitempty"`
	// Jobs is how many jobs the session ran.
	Jobs int `json:"jobs,omitempty"`
	// Credits is what the session spent.
	Credits int `json:"credits,omitempty"`
}

// Event is the full JSON body POSTed to a developer endpoint.
type Event struct {
	// ID is the delivery id, also echoed in [DeliveryHeader].
	ID string `json:"id"`
	// Type is the event type, also echoed in [EventHeader].
	Type EventType `json:"type"`
	// Created is the delivery time, in unix seconds.
	Created int64 `json:"created"`
	// Data is the event payload.
	Data EventData `json:"data"`
}

// CreatedAt returns [Event.Created] as a [time.Time].
func (e *Event) CreatedAt() time.Time { return time.Unix(e.Created, 0).UTC() }

// config holds the resolved [Option] values.
type config struct {
	tolerance time.Duration
	now       time.Time
}

// Option tunes [Verify], [Valid] and [ConstructEvent].
type Option func(*config)

// WithTolerance rejects a delivery whose signed timestamp is more than d away
// from now — replay protection. Off by default; five minutes is the
// conventional choice.
func WithTolerance(d time.Duration) Option {
	return func(c *config) { c.tolerance = d }
}

// WithNow fixes the clock the tolerance check compares against. Intended for
// deterministic tests.
func WithNow(t time.Time) Option {
	return func(c *config) { c.now = t }
}

// parsedHeader is a decoded `t=…,v1=…[,v1=…]` header.
type parsedHeader struct {
	timestamp  int64
	signatures []string
}

// parseSignatureHeader decodes the comma-separated k=v pairs. Unknown keys are
// ignored, and v1 may appear more than once during a secret rotation.
func parseSignatureHeader(header string) (parsedHeader, error) {
	var out parsedHeader
	var haveTimestamp bool
	for _, part := range strings.Split(header, ",") {
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		value := strings.TrimSpace(part[eq+1:])
		switch key {
		case "t":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				continue
			}
			out.timestamp = parsed
			haveTimestamp = true
		case "v1":
			if value != "" {
				out.signatures = append(out.signatures, strings.ToLower(value))
			}
		}
	}
	if !haveTimestamp || len(out.signatures) == 0 {
		return parsedHeader{}, ErrInvalidHeader
	}
	return out, nil
}

// Sign computes the signature snapnedit would send for payload at timestamp:
// hex(HMAC_SHA256(secret, "<t>.<payload>")). Exported so a test harness can
// produce a valid delivery without a live server.
func Sign(payload []byte, timestamp int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeaderValue formats the header value for a payload — the value
// snapnedit puts in [SignatureHeader].
func SignatureHeaderValue(payload []byte, timestamp int64, secret string) string {
	return fmt.Sprintf("t=%d,v1=%s", timestamp, Sign(payload, timestamp, secret))
}

// Verify authenticates a delivery: it recomputes HMAC-SHA256 over
// "<t>.<payload>" keyed by secret and compares it, in constant time, against
// every v1 in the signature header.
//
// payload MUST be the exact bytes received, verified BEFORE any JSON parse.
// signature is the raw [SignatureHeader] value.
//
// It returns nil when a signature matches (and, with [WithTolerance], when the
// timestamp is fresh), and a non-nil error otherwise — [ErrInvalidHeader],
// [ErrSignatureMismatch] or [ErrTimestampOutOfTolerance]. It never panics on
// untrusted input, so a caller may treat every non-nil error identically.
func Verify(payload []byte, signature, secret string, opts ...Option) error {
	if secret == "" {
		return ErrEmptySecret
	}
	cfg := config{now: time.Now()}
	for _, opt := range opts {
		opt(&cfg)
	}

	parsed, err := parseSignatureHeader(signature)
	if err != nil {
		return err
	}

	if cfg.tolerance > 0 {
		drift := cfg.now.Sub(time.Unix(parsed.timestamp, 0))
		if drift < 0 {
			drift = -drift
		}
		if drift > cfg.tolerance {
			return ErrTimestampOutOfTolerance
		}
	}

	expected := []byte(Sign(payload, parsed.timestamp, secret))
	// Compare against every candidate, and do not stop at the first match: a
	// short-circuit here would leak, through timing, which rotation slot
	// matched.
	matched := false
	for _, candidate := range parsed.signatures {
		if hmac.Equal(expected, []byte(candidate)) {
			matched = true
		}
	}
	if !matched {
		return ErrSignatureMismatch
	}
	return nil
}

// Valid is [Verify] as a boolean, for a receiver that treats every failure
// identically. It mirrors the reference TypeScript verifier's signature.
func Valid(payload []byte, signature, secret string, opts ...Option) bool {
	return Verify(payload, signature, secret, opts...) == nil
}

// ConstructEvent verifies a delivery and then parses it — the one call a
// webhook handler needs. It never parses an unauthenticated payload: a
// verification failure returns before any JSON is read.
func ConstructEvent(payload []byte, signature, secret string, opts ...Option) (*Event, error) {
	if err := Verify(payload, signature, secret, opts...); err != nil {
		return nil, err
	}
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("snapnedit/webhooks: could not parse the delivery body: %w", err)
	}
	return &event, nil
}
