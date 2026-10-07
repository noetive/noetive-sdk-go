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
// governs a description does not govern this: carrying the limit that fired, when it
// clears, the guard that refused and the field that was wrong is nearly free, and
// it is the difference between a plan and a retry loop.
//
// "rate_limited" on its own leaves a caller to guess. "rate_limited, per_hour,
// retry in 41 minutes, 0 of 30 left" is an instruction. So every field the server
// sends is kept and surfaced, and [Error.Error] renders the ones that change what
// happens next.

// The refusal codes. A closed set, because branching on it is the point.
const (
	// CodeUnauthorized means the credential is missing, unknown, revoked or
	// expired. The only refusal that means the key itself does not work.
	CodeUnauthorized = "unauthorized"

	// CodeNotBillable means the key is good and the account behind it cannot be
	// charged. The server answers 402. Retrying will not help until billing is
	// set up.
	CodeNotBillable = "not_billable"

	// CodeForbiddenScope means the key is good and does not reach what was
	// asked: its agent is disabled or not on this service, or it may not watch
	// the mailbox or send as the sender it named. Never returned for an object
	// the caller cannot read — that is CodeNotFound, because saying "forbidden"
	// about an identifier confirms it exists.
	CodeForbiddenScope = "forbidden_scope"

	// CodeNotFound means the object does not exist, exists outside every grant
	// the caller holds, or the operation does not exist.
	CodeNotFound = "not_found"

	// CodePreconditionFailed means other writes to the same object kept landing
	// first. Read it again and retry; an UpdateMessage is safe to repeat as it
	// was. The server answers 409, which it shares with CodePaused.
	CodePreconditionFailed = "precondition_failed"

	// CodePolicyRefused means a guard fired. Guard names which one. Not worth
	// retrying unchanged.
	CodePolicyRefused = "policy_refused"

	// CodeRateLimited means a limit is spent. Counter names which, and RetryAfter
	// says when it clears. With no RetryAfter, waiting will not help — a send
	// with more recipients than the limit allows, say — and the request has to
	// change.
	CodeRateLimited = "rate_limited"

	// CodePaused means the mailbox is paused and is not sending. Unlike
	// CodeRateLimited it does not clear on its own. The server answers 409, which
	// it shares with CodePreconditionFailed, so branch on the code.
	CodePaused = "paused"

	// CodeInvalid means the request was malformed: a field is wrong, the body
	// is over its size or nesting limit, or the method is not POST. Field is a
	// JSON pointer to where, when one field is to blame.
	CodeInvalid = "invalid"

	// CodeUnavailable means this deployment does not serve this option of the
	// operation. Retrying will not help; the server answers 404, so branch on
	// the code rather than the status.
	CodeUnavailable = "unavailable"

	// CodeInternal means the request was fine and something on the server broke.
	// The key is fine. Retrying later may succeed.
	CodeInternal = "internal"

	// CodeUpstreamUnavailable means something bud depends on could not be reached,
	// and the request had no effect. The server answers 503; retry with backoff.
	CodeUpstreamUnavailable = "upstream_unavailable"

	// CodeMalformedResponse is this package's own: the server answered with
	// something that is not the envelope. Distinct from CodeInternal so a caller
	// can tell "the server failed" from "we could not read what it said".
	CodeMalformedResponse = "malformed_response"
)

// Sentinels for errors.Is. Only the code is compared.
var (
	ErrUnauthorized        = &Error{Code: CodeUnauthorized}
	ErrNotBillable         = &Error{Code: CodeNotBillable}
	ErrForbiddenScope      = &Error{Code: CodeForbiddenScope}
	ErrNotFound            = &Error{Code: CodeNotFound}
	ErrPreconditionFailed  = &Error{Code: CodePreconditionFailed}
	ErrPolicyRefused       = &Error{Code: CodePolicyRefused}
	ErrRateLimited         = &Error{Code: CodeRateLimited}
	ErrPaused              = &Error{Code: CodePaused}
	ErrInvalid             = &Error{Code: CodeInvalid}
	ErrUnavailable         = &Error{Code: CodeUnavailable}
	ErrInternal            = &Error{Code: CodeInternal}
	ErrUpstreamUnavailable = &Error{Code: CodeUpstreamUnavailable}
	ErrMalformedResponse   = &Error{Code: CodeMalformedResponse}
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

	// RetryAfterMs is when the limit clears, on CodeRateLimited, and absent when
	// waiting will not help. Read it through RetryAfter.
	RetryAfterMs uint32 `json:"retry_after_ms,omitempty"`

	// Current and Version are declared by the wire for a conflict that carries
	// the object as stored. Nothing served sets them; a conflict says only
	// to read again.
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
// writing the same switch.
//
// True for:
//   - CodeUpstreamUnavailable, which the server promises had no effect, so
//     repeating it cannot duplicate anything;
//   - CodeRateLimited, when it says how long to wait — without RetryAfter the
//     request itself has to change;
//   - CodePreconditionFailed, after reading the object again: other writes
//     landed first, and an UpdateMessage is safe to repeat as it was;
//   - CodeInternal, which makes no promise about effects: retry a Send after
//     it only with the same IdempotencyKey, or the retry can send a second
//     copy.
//
// Note what is false. CodeUnavailable is not retryable however transient it
// looks, because it means this deployment does not serve that option of the
// operation. CodePaused clears only when an operator releases the mailbox, so a
// caller that slept and retried could wait for ever. CodeNotBillable waits on a
// person setting up billing. CodePolicyRefused is not worth repeating unchanged.
func (e *Error) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case CodeUpstreamUnavailable, CodePreconditionFailed, CodeInternal:
		return true
	case CodeRateLimited:
		return e.RetryAfterMs > 0
	default:
		return false
	}
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
// operation's own output — a load balancer's HTML, a proxy's plain text. The status still
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
