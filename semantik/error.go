package semantik

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Error is the typed API error surfaced from every SDK method when the
// server returns a structured JSON error envelope. Transport errors
// (DNS, TCP, TLS, ctx.Err()) are returned raw from [net/http] and are
// NOT wrapped in Error.
//
// Match with [errors.Is] against a sentinel to test for a specific code:
//
//	if errors.Is(err, semantik.ErrBackpressure) { ... }
//
// Extract full detail with [errors.As]:
//
//	var apiErr *semantik.Error
//	if errors.As(err, &apiErr) { log.Print(apiErr.HTTPStatus) }
//
// Only Code is inspected by [Error.Is]; treat Message, RetryAfter,
// HTTPStatus and RequestID as read-only diagnostic fields that may be
// zero-valued if the server response was partial or malformed.
//
// HTTPStatus == 0 signals that the SDK's own pre-flight validation
// rejected the request before sending it. The adjacent case is a
// malformed 2xx decode failure, which surfaces as Code ==
// [CodeMalformedResponse] with HTTPStatus preserved from the wire —
// that lets callers tell "we rejected before sending" apart from
// "server responded but the body was unparseable."
//
// Field ordering: strings (16 B each) > time.Duration (8 B) > int (8 B).
type Error struct {
	// Code is the machine-readable error code from the server's JSON
	// envelope (e.g. CodeRateLimited). See the Code* constants.
	Code string

	// Message is the server's human-readable description. May be empty.
	Message string

	// RequestID is the server-assigned correlation token, taken from the
	// response body's request_id field (preferred) or the X-Request-Id
	// header (fallback). Quote it when contacting support.
	RequestID string

	// RetryAfter is the server's retry hint. Zero means do not retry.
	// Read from the retry_after_ms body field when present and the
	// Retry-After header otherwise, on any error response that carries
	// one — most often HTTP 429 (CodeBackpressure) and HTTP 503
	// (CodeUnavailable), but also HTTP 400 CodeModelNotProvisioned,
	// where the hint is what marks the condition as waitable.
	RetryAfter time.Duration

	// HTTPStatus is the numeric status code. Zero indicates the Error
	// was produced by SDK pre-flight validation (never reached the wire).
	HTTPStatus int
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.HTTPStatus == 0 && e.Message != "":
		return fmt.Sprintf("semantik: %s: %s", e.Code, e.Message)
	case e.HTTPStatus == 0:
		return fmt.Sprintf("semantik: %s", e.Code)
	case e.Message != "":
		return fmt.Sprintf("semantik: %d %s: %s", e.HTTPStatus, e.Code, e.Message)
	default:
		return fmt.Sprintf("semantik: %d %s", e.HTTPStatus, e.Code)
	}
}

// Is reports whether target is an *Error with the same Code. All other
// fields on the sentinel are ignored so that errors.Is match is based
// on the error code alone — the idiomatic Go contract.
func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// Machine-readable error codes returned by the Semantik API. See the
// "Error codes" table in public-api.yaml.
//
// # Forward compatibility
//
// The SDK preserves any [Error.Code] value the server returns,
// including codes added in future server versions that this SDK has
// not yet enumerated below. Switch statements over [Error.Code]
// should always include a default arm so an unknown code does not
// silently misroute. Use [IsKnownCode] to test whether a given code
// belongs to the set this SDK release recognises.
const (
	CodeInvalidRequest       = "invalid_request"
	CodeUnauthorized         = "unauthorized"
	CodeNotBillable          = "not_billable"
	CodeMethodNotAllowed     = "method_not_allowed"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeRequestTooLarge      = "request_too_large"
	CodeRateLimited          = "rate_limited"
	CodeTooManyRequests      = "too_many_requests"
	CodeBackpressure         = "backpressure"
	CodeUnavailable          = "unavailable"
	CodeNamespaceUnavailable = "namespace_unavailable"
	CodeNamespaceDisabled    = "namespace_disabled"
	CodeModelNotProvisioned  = "model_not_provisioned"
	CodeMeteringUnavailable  = "metering_unavailable"
	CodeInternalError        = "internal_error"

	// CodeMalformedResponse is a client-side code returned when the
	// server replied with a 2xx status but a body the SDK could not
	// decode. Not part of the wire protocol.
	CodeMalformedResponse = "malformed_response"
)

// IsKnownCode reports whether code is one of the wire-protocol error
// codes this SDK release enumerates. Returns false for unknown codes
// (e.g. introduced by a newer server version) — those still flow
// through as the verbatim string on [Error.Code] but cannot be
// matched against the Code* sentinels.
//
// Use to gate code-specific branching:
//
//	if !semantik.IsKnownCode(apiErr.Code) {
//	    // Treat as a generic API error; do not crash on a future code.
//	}
func IsKnownCode(code string) bool {
	switch code {
	case CodeInvalidRequest,
		CodeUnauthorized,
		CodeNotBillable,
		CodeMethodNotAllowed,
		CodeUnsupportedMediaType,
		CodeRequestTooLarge,
		CodeRateLimited,
		CodeTooManyRequests,
		CodeBackpressure,
		CodeUnavailable,
		CodeNamespaceUnavailable,
		CodeNamespaceDisabled,
		CodeModelNotProvisioned,
		CodeMeteringUnavailable,
		CodeInternalError,
		CodeMalformedResponse:
		return true
	default:
		return false
	}
}

// Package sentinel errors. Each is an *Error skeleton usable only as
// the target of [errors.Is] — field values other than Code are ignored
// during comparison.
var (
	ErrInvalidRequest       = &Error{Code: CodeInvalidRequest}
	ErrUnauthorized         = &Error{Code: CodeUnauthorized}
	ErrNotBillable          = &Error{Code: CodeNotBillable}
	ErrMethodNotAllowed     = &Error{Code: CodeMethodNotAllowed}
	ErrUnsupportedMediaType = &Error{Code: CodeUnsupportedMediaType}
	ErrRequestTooLarge      = &Error{Code: CodeRequestTooLarge}
	ErrRateLimited          = &Error{Code: CodeRateLimited}
	ErrTooManyRequests      = &Error{Code: CodeTooManyRequests}
	ErrBackpressure         = &Error{Code: CodeBackpressure}
	ErrUnavailable          = &Error{Code: CodeUnavailable}
	ErrNamespaceUnavailable = &Error{Code: CodeNamespaceUnavailable}
	ErrNamespaceDisabled    = &Error{Code: CodeNamespaceDisabled}
	ErrModelNotProvisioned  = &Error{Code: CodeModelNotProvisioned}
	ErrMeteringUnavailable  = &Error{Code: CodeMeteringUnavailable}
	ErrInternal             = &Error{Code: CodeInternalError}
)

// Structural errors not bound to a server code.
var (
	// ErrInvalidAPIKey is returned by New when the supplied key does
	// not carry a recognised Noetive prefix.
	ErrInvalidAPIKey = errors.New("semantik: invalid API key format")

	// ErrMissingAPIKey is returned by NewFromEnv when the
	// NOETIVE_KEY_SECRET environment variable is unset or empty.
	ErrMissingAPIKey = errors.New("semantik: NOETIVE_KEY_SECRET not set")

	// ErrAuthorizationWithKey is returned by New when [WithAuthorization]
	// is passed alongside an API key. New sends the key it was given;
	// a forwarded credential belongs with [NewForwarding].
	ErrAuthorizationWithKey = errors.New("semantik: WithAuthorization is for NewForwarding, not New")

	// ErrMalformedSSE matches any malformed-stream error surfaced by
	// the SDK. Use with [errors.Is]; to inspect the underlying parse
	// cause, use [errors.As] against a *[MalformedSSEError].
	ErrMalformedSSE = errors.New("semantik: malformed SSE frame")
)

// MalformedSSEError is the typed form of [ErrMalformedSSE]. Extract
// it with [errors.As] to see what the SDK rejected:
//
//	var mse *semantik.MalformedSSEError
//	if errors.As(err, &mse) { log.Printf("reason: %v", mse.Err) }
//
// The struct exists alongside the sentinel so that
// errors.Is(err, ErrMalformedSSE) keeps matching — callers that only
// need the classification don't have to change.
type MalformedSSEError struct {
	// Err is the underlying cause (JSON decode failure, wrong
	// content-type, missing field, etc.). Always non-nil.
	Err error
}

func (e *MalformedSSEError) Error() string {
	if e == nil || e.Err == nil {
		return ErrMalformedSSE.Error()
	}
	return ErrMalformedSSE.Error() + ": " + e.Err.Error()
}

// Is reports whether target is [ErrMalformedSSE]. The wrapped cause
// is reachable through [MalformedSSEError.Unwrap]; matching against a
// different sentinel falls through to the wrapped error.
func (e *MalformedSSEError) Is(target error) bool {
	return target == ErrMalformedSSE
}

// Unwrap returns the underlying cause so callers can dig further with
// [errors.Is] / [errors.As].
func (e *MalformedSSEError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// SubscribeSetupError is returned from [Client.Subscribe] when the
// subscription handshake — the POST, the content-type check, and the
// initial "subscribed" SSE frame — fails terminally (after the
// configured [RetryPolicy] has exhausted its attempts, or
// immediately for non-retryable codes like CodeUnauthorized).
//
// Setup-time failures are distinct from in-flight failures (see
// [SubscribeStreamError]) because the remediation is different:
// setup is safe to retry — the server either commits state or it
// doesn't — and the SDK does so automatically per the configured
// policy; in-flight is not, because a silent reconnect would drop
// matches between the old and new subscription_id.
//
// Err is the structured server envelope when available (so
// [errors.Is](err, [ErrUnauthorized]), [errors.As] against [*Error],
// and access to RetryAfter / RequestID all continue to work). Cause
// is the raw underlying error — typically the same value as Err for
// API-shaped failures, but for transport errors (TCP reset during
// handshake, content-type mismatch) it carries the original
// non-[*Error] type so [errors.As] against [*MalformedSSEError] etc.
// still resolves through the Unwrap chain.
//
// Field ordering: pointer (8 B) > error interface (16 B).
type SubscribeSetupError struct {
	Err   *Error
	Cause error
}

// Error implements the error interface.
func (e *SubscribeSetupError) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.Cause != nil:
		return "semantik: subscribe setup: " + e.Cause.Error()
	case e.Err != nil:
		return "semantik: subscribe setup: " + e.Err.Error()
	default:
		return "semantik: subscribe setup failed"
	}
}

// Unwrap returns the underlying cause so [errors.Is] and [errors.As]
// can peel back to the original [*Error] / [*MalformedSSEError] /
// transport error.
func (e *SubscribeSetupError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return e.Cause
	}
	if e.Err != nil {
		return e.Err
	}
	return nil
}

// SubscribeStreamError is returned from [Subscription.Next] when the
// SSE stream drops or yields a malformed frame *after* the
// "subscribed" event has been delivered.
//
// The SDK never auto-reconnects a mid-stream failure: a transparent
// reconnect would silently drop matches between the old and new
// subscription_id. The caller decides whether to reconnect (and how
// to dedupe via message_id) on receiving this error.
//
// io.EOF — a clean server-side close — is NOT wrapped in
// SubscribeStreamError; it surfaces unchanged so callers can use
// [errors.Is](err, [io.EOF]).
//
// Cause is the underlying transport or parse error; Err is the
// best-effort structured shape when one can be synthesised (e.g.
// CodeMalformedResponse for a parse failure).
//
// Field ordering: pointer (8 B) > error interface (16 B).
type SubscribeStreamError struct {
	Err   *Error
	Cause error
}

// Error implements the error interface.
func (e *SubscribeStreamError) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.Cause != nil:
		return "semantik: subscribe stream: " + e.Cause.Error()
	case e.Err != nil:
		return "semantik: subscribe stream: " + e.Err.Error()
	default:
		return "semantik: subscribe stream failed"
	}
}

// Unwrap returns the underlying cause so [errors.Is] / [errors.As]
// can resolve through to the original error.
func (e *SubscribeStreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return e.Cause
	}
	if e.Err != nil {
		return e.Err
	}
	return nil
}

// errorEnvelope is the wire shape of the ErrorResponse schema.
type errorEnvelope struct {
	Err          string `json:"error"`
	Message      string `json:"message,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	RetryAfterMs uint32 `json:"retry_after_ms,omitempty"`
}

// decodeError reads the body of a non-2xx response, parses the JSON
// error envelope, and returns an *Error. It is tolerant of an empty or
// malformed body — the returned *Error will still carry HTTPStatus.
//
// RetryAfter is populated from either the `retry_after_ms` body field
// or the HTTP `Retry-After` header (in seconds, per RFC 9110). The
// body field takes precedence when both are present; the header is the
// fallback for proxies or SDKs that only see headers.
//
// The caller is responsible for closing resp.Body.
func decodeError(resp *http.Response) *Error {
	e := &Error{HTTPStatus: resp.StatusCode}
	// X-Request-Id is set on every response. Capture it as a header-only
	// fallback so even bodyless / malformed responses preserve correlation.
	e.RequestID = resp.Header.Get("X-Request-Id")
	// Cap read to avoid unbounded memory on a malicious/broken proxy.
	// 64 KB is larger than any legitimate error body.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if len(data) == 0 {
		e.Code = statusCodeFallback(resp.StatusCode)
		e.RetryAfter = retryAfterFromHeader(resp)
		return e
	}
	var env errorEnvelope
	if err := safeUnmarshal(data, &env); err != nil {
		e.Code = statusCodeFallback(resp.StatusCode)
		// Preserve a short excerpt of the malformed body for diagnosis
		// without holding a reference to a potentially large buffer.
		const maxMsgBytes = 256
		if len(data) <= maxMsgBytes {
			e.Message = string(data)
		} else {
			e.Message = fmt.Sprintf("malformed body (%d bytes; first %d: %q)",
				len(data), maxMsgBytes, data[:maxMsgBytes])
		}
		e.RetryAfter = retryAfterFromHeader(resp)
		return e
	}
	if env.Err == "" {
		e.Code = statusCodeFallback(resp.StatusCode)
	} else {
		e.Code = env.Err
	}
	e.Message = env.Message
	// Body request_id wins over header (they should match, but the body
	// is what the server canonically committed to).
	if env.RequestID != "" {
		e.RequestID = env.RequestID
	}
	e.RetryAfter = retryAfterFromBodyMs(env.RetryAfterMs)
	if e.RetryAfter == 0 {
		e.RetryAfter = retryAfterFromHeader(resp)
	}
	return e
}

// retryAfterFromBodyMs converts a retry_after_ms JSON field to a
// [time.Duration]. A value larger than [maxRetryAfterMs] is discarded
// rather than trusted: a malicious or misconfigured server emitting the
// uint32 maximum (~49 days) would otherwise park a retrying caller for
// weeks. The cap matches the header path's defence in depth.
func retryAfterFromBodyMs(ms uint32) time.Duration {
	if ms == 0 || int64(ms) > maxRetryAfterMs {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// maxRetryAfterMs is a conservative ceiling on any retry hint the SDK
// will accept from the server. One hour is far longer than any
// legitimate transient-retry hint and far short of uint32 max.
const maxRetryAfterMs = int64(time.Hour / time.Millisecond)

// retryAfterFromHeader parses the HTTP Retry-After header per RFC 9110
// §10.2.3. Both forms are honoured:
//
//   - delta-seconds: an integer number of seconds (e.g. "120").
//   - HTTP-date: an absolute timestamp (e.g.
//     "Wed, 21 Oct 2026 07:28:00 GMT"). The returned duration is
//     time.Until(parsed); past dates return 0.
//
// Returns 0 when the header is missing, empty, zero, negative,
// unparseable, or larger than can be represented as a
// [time.Duration]. Overflow protection matters because a malicious or
// misconfigured intermediary may emit a retry hint so large it
// wraps int64 nanoseconds into a negative value.
func retryAfterFromHeader(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 || int64(secs) > maxRetryAfterSecs {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			// Past date, or far-future date that overflowed into
			// negative territory during time.Sub.
			return 0
		}
		return d
	}
	return 0
}

// maxRetryAfterSecs is the largest integer second value that fits in
// a [time.Duration] without overflow. ~292 years.
const maxRetryAfterSecs = int64(^uint64(0)>>1) / int64(time.Second)

// statusCodeFallback derives a best-guess Code when the server body is
// empty or unparseable. Keeps errors.Is usable even against misbehaving
// intermediaries.
//
// Deliberate asymmetry on 429: the wire protocol has two distinct 429
// codes, CodeBackpressure (retryable with hint) and CodeRateLimited
// (terminal). When the server body is absent there is no way to tell
// which was meant, so the fallback picks CodeRateLimited — blind
// retries of a rate-limit can get the caller blocked harder, while
// forgoing a retry on a stripped backpressure response merely loses a
// chance to recover. A [TransientRetry] caller that wants the
// backpressure path must ensure the server / intermediaries preserve
// the JSON body.
func statusCodeFallback(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeInvalidRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusPaymentRequired:
		return CodeNotBillable
	case http.StatusForbidden:
		return CodeNamespaceDisabled
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusRequestEntityTooLarge:
		return CodeRequestTooLarge
	case http.StatusUnsupportedMediaType:
		return CodeUnsupportedMediaType
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusServiceUnavailable:
		return CodeUnavailable
	default:
		return CodeInternalError
	}
}
