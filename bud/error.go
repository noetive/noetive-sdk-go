package bud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Refusals, and why this package spends so much on them.
//
// A refusal is read once, by whoever is about to decide what to do next. It is
// not re-sent on every turn the way a tool manifest is, so the budget that
// governs a description does not govern this: carrying the limit that fired, the
// version to retry with and the object as it now stands is nearly free, and it is
// the difference between a plan and a retry loop.
//
// "rate_limited" on its own leaves a caller to guess. "rate_limited, per_hour,
// retry in 41 minutes, 0 of 30 left" is an instruction. So every field the server
// sends is kept and surfaced, and [Error.Error] renders the ones that change what
// happens next.

// The refusal codes. A closed set, because branching on it is the point.
const (
	// CodeUnauthorized means the token is missing, unknown or expired.
	CodeUnauthorized = "unauthorized"

	// CodeForbiddenScope means the token is good and lacks the scope this object
	// needs. Never returned for an object the caller cannot see at all — that is
	// CodeNotFound, because saying "forbidden" about an identifier confirms it
	// exists.
	CodeForbiddenScope = "forbidden_scope"

	// CodeNotFound means the object does not exist, or exists outside every
	// grant the caller holds.
	CodeNotFound = "not_found"

	// CodePreconditionFailed means the version did not match. The refusal carries
	// Current and Version, so the retry needs no extra read.
	CodePreconditionFailed = "precondition_failed"

	// CodePolicyRefused means a guard fired. Guard names which one.
	CodePolicyRefused = "policy_refused"

	// CodeRateLimited means a sending limit is exhausted. Counter names which and
	// RetryAfter says when it clears.
	CodeRateLimited = "rate_limited"

	// CodePaused means an operator or an anomaly check paused the mailbox. Unlike
	// CodeRateLimited it does not clear on its own.
	CodePaused = "paused"

	// CodeInvalid means the request was malformed. Field is a JSON pointer to
	// where.
	CodeInvalid = "invalid"

	// CodeUnavailable means this deployment does not implement the operation.
	// Retrying will not help until it is redeployed; the server answers 501.
	CodeUnavailable = "unavailable"

	// CodeInternal means the request was fine and something on the server broke.
	// The one code where retrying later is the right advice.
	CodeInternal = "internal"

	// CodeMalformedResponse is this package's own: the server answered with
	// something that is not the envelope. Distinct from CodeInternal so a caller
	// can tell "the server failed" from "we could not read what it said".
	CodeMalformedResponse = "malformed_response"
)

// Sentinels for errors.Is. Only the code is compared.
var (
	ErrUnauthorized       = &Error{Code: CodeUnauthorized}
	ErrForbiddenScope     = &Error{Code: CodeForbiddenScope}
	ErrNotFound           = &Error{Code: CodeNotFound}
	ErrPreconditionFailed = &Error{Code: CodePreconditionFailed}
	ErrPolicyRefused      = &Error{Code: CodePolicyRefused}
	ErrRateLimited        = &Error{Code: CodeRateLimited}
	ErrPaused             = &Error{Code: CodePaused}
	ErrInvalid            = &Error{Code: CodeInvalid}
	ErrUnavailable        = &Error{Code: CodeUnavailable}
	ErrInternal           = &Error{Code: CodeInternal}
	ErrMalformedResponse  = &Error{Code: CodeMalformedResponse}
)

// Error is a refusal, with everything the server said about what to do next.
type Error struct {
	// Code is what to branch on.
	Code string `json:"code"`

	// Message is the server's own sentence. It never quotes anything a sender
	// wrote: the server holds every refusal to a fixed vocabulary precisely
	// because an error is the one place a reader treats as the server speaking.
	Message string `json:"message"`

	// Hint says what would unblock this, in the server's words.
	Hint string `json:"hint,omitempty"`

	// RequestID correlates this refusal with everything the request caused,
	// including the journal events. Quote it when asking for help.
	RequestID string `json:"request_id,omitempty"`

	// RetryAfterMs is when the limit clears, on CodeRateLimited. Read it through
	// RetryAfter.
	RetryAfterMs uint32 `json:"retry_after_ms,omitempty"`

	// Current and Version are set on CodePreconditionFailed: the object as
	// stored, and the version to quote on the retry. Together they make a
	// conflict recoverable without a second read.
	Current json.RawMessage `json:"current,omitempty"`
	Version string          `json:"version,omitempty"`

	// Field is a JSON pointer into the request, on CodeInvalid.
	Field string `json:"field,omitempty"`

	// Guard is the policy that fired, on CodePolicyRefused.
	Guard string `json:"guard,omitempty"`

	// Counter is the limit that refused, on CodeRateLimited.
	Counter string `json:"counter,omitempty"`

	// HTTPStatus is the status the refusal arrived with. Zero means this package
	// produced the refusal before anything was sent.
	HTTPStatus int `json:"-"`
}

// Error renders the refusal as a sentence that says what to do next.
//
// Deliberately more than the code and the message. A refusal is read once, by
// somebody deciding, so the fields that change that decision belong in the line
// they will actually see rather than behind a type assertion they may not make.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	var b strings.Builder
	b.WriteString("bud: ")
	if e.HTTPStatus > 0 {
		fmt.Fprintf(&b, "%d ", e.HTTPStatus)
	}
	b.WriteString(e.Code)
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}

	// The fields that are a plan rather than a description.
	if e.Counter != "" {
		b.WriteString(" [limit " + e.Counter)
		if d := e.RetryAfter(); d > 0 {
			b.WriteString(", retry in " + d.Round(time.Second).String())
		}
		b.WriteString("]")
	}
	if e.Guard != "" {
		b.WriteString(" [guard " + e.Guard + "]")
	}
	if e.Field != "" {
		b.WriteString(" [at " + e.Field + "]")
	}
	if e.Version != "" {
		b.WriteString(" [retry with version " + e.Version + "]")
	}
	if e.Hint != "" {
		b.WriteString(" — " + e.Hint)
	}
	if e.RequestID != "" {
		b.WriteString(" (" + e.RequestID + ")")
	}
	return b.String()
}

// Is compares the code and nothing else, so a sentinel matches a populated
// refusal.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e != nil && other != nil && e.Code == other.Code
}

// RetryAfter is when the limit clears, as a duration. Zero means the server gave
// no hint, which is not the same as "retry now".
func (e *Error) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return time.Duration(e.RetryAfterMs) * time.Millisecond
}

// Retryable reports whether waiting and trying again could succeed.
//
// A judgment this package is willing to make because the server's codes are
// specific enough to support it, and because the alternative is every caller
// writing the same switch. Note what is false: CodeUnavailable is not retryable
// however transient it looks, because its own hint says the deployment does not
// implement the operation.
func (e *Error) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case CodeRateLimited, CodePaused, CodeInternal:
		return true
	default:
		return false
	}
}

// Into decodes the object a conflict carried into v.
//
// The point of Current: after a precondition failure the caller already has the
// object as stored and the version to quote, so the retry is a merge rather than
// a second round trip.
func (e *Error) Into(v any) error {
	if e == nil || len(e.Current) == 0 {
		return errors.New("bud: this refusal carries no current object")
	}
	return json.Unmarshal(e.Current, v)
}

// preflight builds a refusal this package produced before sending anything.
//
// HTTPStatus stays zero, which is how a caller tells "we rejected this" from
// "the server did".
func preflight(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// errorFrom reads a refusal out of a response that is not the envelope.
//
// Used when the status says failure and the body does not decode into the
// operation's own output — an ALB's HTML, a proxy's plain text. The status still
// carries information, so it is kept.
func errorFrom(status int, body []byte, requestID string) *Error {
	e := &Error{
		Code:       CodeMalformedResponse,
		Message:    "the server's answer was not an envelope",
		HTTPStatus: status,
		RequestID:  requestID,
	}
	// A short body is worth surfacing; a long one is a web page. Neither is
	// trusted as the server's own voice, so it is labelled.
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" && len(trimmed) <= 200 {
		e.Hint = "the body was: " + trimmed
	}
	if status == http.StatusUnauthorized {
		e.Code = CodeUnauthorized
		e.Message = "the credential was not accepted"
	}
	return e
}
