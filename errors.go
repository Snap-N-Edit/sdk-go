package snapnedit

import (
	"errors"
	"fmt"
	"time"
)

// ErrorCode is the machine-readable code carried by every snapnedit API
// failure. It comes from a closed set — see [ErrorCodes] — and is the field an
// SDK consumer should branch on. Error messages are human text and change.
type ErrorCode string

// Every error code the API can return. Mirrors the `ErrorEnvelope` schema in
// the published OpenAPI document.
const (
	// CodeInvalidInput means the request body failed validation: a bad param
	// value, an unrecognized param key (every operation's schema is strict), a
	// missing mask, or a malformed field.
	CodeInvalidInput ErrorCode = "invalid_input"
	// CodeUnsupportedMime means the declared upload MIME type is not one the
	// API accepts (image/png, image/jpeg, image/webp).
	CodeUnsupportedMime ErrorCode = "unsupported_mime"
	// CodeTooLarge means the image exceeded the upload size ceiling.
	CodeTooLarge ErrorCode = "too_large"
	// CodeNotFound means the resource does not exist — or belongs to another
	// account. The API answers 404 rather than 403 for a foreign id, so that an
	// id can never become an existence oracle.
	CodeNotFound ErrorCode = "not_found"
	// CodeInputFetchFailed means a job whose input was an external URL could
	// not be fetched (blocked address, redirect, timeout, non-2xx, oversized,
	// or not an image). Terminal, and the credits are refunded.
	CodeInputFetchFailed ErrorCode = "input_fetch_failed"
	// CodeProviderFailed means a model provider errored on this job.
	CodeProviderFailed ErrorCode = "provider_failed"
	// CodeProviderExhausted means every provider capable of the operation
	// failed.
	CodeProviderExhausted ErrorCode = "provider_exhausted"
	// CodeRateLimited means the caller exceeded a rate limit. Retryable.
	CodeRateLimited ErrorCode = "rate_limited"
	// CodeBotCheckFailed means bot protection rejected the request.
	CodeBotCheckFailed ErrorCode = "bot_check_failed"
	// CodeUnauthorized means no credential was presented, or the credential is
	// unknown or revoked.
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeForbidden means the credential is valid but not allowed to do this —
	// for example an anonymous caller naming an inputUrl or a destination.
	CodeForbidden ErrorCode = "forbidden"
	// CodePaymentRequired means the account has insufficient credits, or a
	// free-tier limit was reached.
	CodePaymentRequired ErrorCode = "payment_required"
	// CodeInternal means an unexpected server-side failure. It is also this
	// client's fallback for a transport failure or an unrecognized code.
	CodeInternal ErrorCode = "internal"
)

// ErrorCodes is every [ErrorCode] the API can return, in the order the
// OpenAPI document lists them.
var ErrorCodes = []ErrorCode{
	CodeInvalidInput,
	CodeUnsupportedMime,
	CodeTooLarge,
	CodeNotFound,
	CodeInputFetchFailed,
	CodeProviderFailed,
	CodeProviderExhausted,
	CodeRateLimited,
	CodeBotCheckFailed,
	CodeUnauthorized,
	CodeForbidden,
	CodePaymentRequired,
	CodeInternal,
}

// IsKnownErrorCode reports whether code is one of the codes this SDK version
// knows about. An unknown code is not a protocol violation: it degrades to
// [CodeInternal] when a response is parsed, so a future server-side addition
// never breaks an older client.
func IsKnownErrorCode(code ErrorCode) bool {
	for _, known := range ErrorCodes {
		if known == code {
			return true
		}
	}
	return false
}

// asErrorCode narrows an arbitrary response field to a known [ErrorCode],
// falling back to [CodeInternal] rather than trusting an unrecognized value.
func asErrorCode(value string) ErrorCode {
	if code := ErrorCode(value); IsKnownErrorCode(code) {
		return code
	}
	return CodeInternal
}

// Error is the typed error every API call and every terminally failed job
// produces. Status is the HTTP status that carried it, or 0 for a transport
// failure that never reached the server.
//
// Compare with [errors.Is] against one of the package sentinels
// ([ErrInvalidInput], [ErrNotFound], …) to branch on the code, or unwrap with
// [errors.As] to read Status and Message:
//
//	if errors.Is(err, snapnedit.ErrRateLimited) { ... }
//
//	var apiErr *snapnedit.Error
//	if errors.As(err, &apiErr) { log.Println(apiErr.Status, apiErr.Message) }
type Error struct {
	// Code is the machine-readable code. Always set.
	Code ErrorCode
	// Message is the server's human-readable explanation. Never parse it.
	Message string
	// Status is the HTTP status code, or 0 when the request never completed.
	Status int
	// Err is the underlying transport error, if any.
	Err error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("snapnedit: %s (http %d): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("snapnedit: %s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying transport error, so [errors.Is] can reach
// things like [context.DeadlineExceeded].
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target describes the same failure. A target [*Error]
// matches when its non-zero fields all match this error's — which is what
// makes the package sentinels (which set only Code) usable with [errors.Is].
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	if other.Code != "" && other.Code != e.Code {
		return false
	}
	if other.Status != 0 && other.Status != e.Status {
		return false
	}
	return true
}

// Sentinel errors, one per [ErrorCode], for use with [errors.Is]. They carry
// no message or status: matching is on the code alone.
var (
	ErrInvalidInput      = &Error{Code: CodeInvalidInput}
	ErrUnsupportedMime   = &Error{Code: CodeUnsupportedMime}
	ErrTooLarge          = &Error{Code: CodeTooLarge}
	ErrNotFound          = &Error{Code: CodeNotFound}
	ErrInputFetchFailed  = &Error{Code: CodeInputFetchFailed}
	ErrProviderFailed    = &Error{Code: CodeProviderFailed}
	ErrProviderExhausted = &Error{Code: CodeProviderExhausted}
	ErrRateLimited       = &Error{Code: CodeRateLimited}
	ErrBotCheckFailed    = &Error{Code: CodeBotCheckFailed}
	ErrUnauthorized      = &Error{Code: CodeUnauthorized}
	ErrForbidden         = &Error{Code: CodeForbidden}
	ErrPaymentRequired   = &Error{Code: CodePaymentRequired}
	ErrInternal          = &Error{Code: CodeInternal}
)

// ErrTimeout is what a [*TimeoutError] unwraps to, so a caller can write
// errors.Is(err, snapnedit.ErrTimeout) without naming the concrete type.
var ErrTimeout = errors.New("snapnedit: timed out")

// TimeoutError is returned when polling a job exceeds its budget without the
// job reaching a settled state. It is deliberately not an [*Error]: a
// client-side poll timeout is not an API response, so there is no HTTP status
// or [ErrorCode] to attach.
type TimeoutError struct {
	// JobID is the job that was being polled.
	JobID string
	// Timeout is the budget that was exceeded.
	Timeout time.Duration
}

// Error implements the error interface.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("snapnedit: polling job %s exceeded %s without reaching a terminal state", e.JobID, e.Timeout)
}

// Unwrap returns [ErrTimeout].
func (e *TimeoutError) Unwrap() error { return ErrTimeout }

// newError builds an [*Error] for a failure that never produced an API error
// envelope (a transport failure, a malformed body).
func newError(code ErrorCode, status int, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Err: cause, Message: fmt.Sprintf(format, args...)}
}
