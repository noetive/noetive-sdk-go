package semantik

import (
	"context"
	"errors"
	"time"
)

// RetryPolicy decides whether a failed request should be retried and
// how long to wait before the next attempt. The interface exists so
// that callers can plug in bespoke policies through [WithRetry] —
// e.g. a token-bucket shaper or a domain-specific jitter schedule —
// without having to fork the SDK.
//
// ShouldRetry is invoked with the zero-based attempt number (0 for the
// first retry decision) and the error returned by the round trip. It
// returns the delay to wait and a boolean indicating whether to retry.
//
// The context passed to the original SDK call bounds the whole retry
// loop: the delay returned here is honoured until ctx.Done fires,
// whichever comes first. Callers that need a separate per-attempt
// budget should derive fresh sub-contexts in their own wrapper around
// the SDK call.
//
// A delay the remaining budget cannot cover — the wait itself plus an
// attempt after it, estimated from the attempt that just failed — is
// not slept out. The error that asked for the wait is returned instead,
// so a caller whose budget is too short to wait still learns what the
// server said and when to come back, rather than only that time ran
// out. When the budget ends during a wait that did fit, both errors are
// returned joined: errors.Is against the context error and errors.As
// against [Error] each reach their answer.
type RetryPolicy interface {
	ShouldRetry(attempt int, err error) (time.Duration, bool)
}

// NoRetry is a [RetryPolicy] that never retries. Pass its zero value
// to [WithRetry] to opt out of the SDK's default single transient
// retry — useful when the caller wraps requests in its own retry
// loop or needs strict one-shot semantics:
//
//	c, _ := semantik.New(key, semantik.WithRetry(semantik.NoRetry{}))
//
// NoRetry is an exported struct rather than a constructor function so
// callers can use the zero value directly without an allocation or
// extra import.
type NoRetry struct{}

// ShouldRetry implements [RetryPolicy] and always returns (0, false).
func (NoRetry) ShouldRetry(int, error) (time.Duration, bool) { return 0, false }

// TransientRetry retries up to max times on transient server
// conditions the API documents as safe to re-issue:
//
//   - [CodeBackpressure]
//   - [CodeUnavailable]
//   - [CodeNamespaceUnavailable]
//   - [CodeMeteringUnavailable]
//
// The server's retry hint ([Error.RetryAfter]) is honoured when
// present; when the hint is missing the SDK falls back to a
// 100 ms / 2 s / 5 s / 10 s schedule, saturating at 10 s for later
// attempts. The first wait is intentionally short — most transient
// pushback resolves within a hundred milliseconds — and later waits
// give a struggling dependency real recovery time before the next
// attempt.
//
// Errors outside this set are never retried. In particular
// [CodeNotBillable] is terminal (retrying will keep failing) and
// [CodeRateLimited] / [CodeTooManyRequests] signal a hard rate-limit
// where blind retries are harmful.
//
// max must be > 0; a zero or negative value disables retry. This
// policy is safe to pair with Publish requests that carry an
// [PublishRequest.IdempotencyKey]; retrying a publish without one
// risks duplicate delivery.
func TransientRetry(max int) RetryPolicy {
	if max <= 0 {
		return NoRetry{}
	}
	return transientRetry{max: max}
}

// Field ordering: int (8 B).
type transientRetry struct {
	max int
}

// transientBackoffSchedule is the fallback delay sequence used when
// the server omits a retry hint. The first wait is short (100 ms) —
// the common case is a sub-second transient that resolves quickly —
// then 2 s and 5 s to give a struggling dependency real recovery
// time, and 10 s as the steady-state cap for later attempts. Indices
// beyond the last entry saturate at the final value.
var transientBackoffSchedule = [...]time.Duration{
	100 * time.Millisecond,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

func (r transientRetry) ShouldRetry(attempt int, err error) (time.Duration, bool) {
	if attempt >= r.max {
		return 0, false
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	switch apiErr.Code {
	case CodeBackpressure,
		CodeUnavailable,
		CodeNamespaceUnavailable,
		CodeMeteringUnavailable:
		// Prefer the server's hint; fall back to the bounded
		// schedule when the server, a proxy, or a truncated body
		// dropped it.
		if d := apiErr.RetryAfter; d > 0 {
			return d, true
		}
		return transientBackoff(attempt), true
	default:
		// Forward-compat: any code not in the retryable set above —
		// including codes added by future server versions that the SDK
		// has not yet enumerated — falls through to "do not retry".
		// See [IsKnownCode] and the Code* constants for the wire
		// vocabulary the SDK recognises today.
		return 0, false
	}
}

// transientBackoff returns the delay for the nth retry attempt when
// the server omitted a retry hint, walking transientBackoffSchedule
// and saturating at its final entry for later attempts.
func transientBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(transientBackoffSchedule) {
		attempt = len(transientBackoffSchedule) - 1
	}
	return transientBackoffSchedule[attempt]
}

// runWithRetry invokes fn until it succeeds, the policy stops retrying,
// or ctx is done. attempt starts at 0 for the first call.
func (c *Client) runWithRetry(ctx context.Context, fn func(attempt int) error) error {
	_, err := runWithRetryValue(ctx, c.retry, func(attempt int) (struct{}, error) {
		return struct{}{}, fn(attempt)
	})
	return err
}

// runWithRetryValue is the generic form of [Client.runWithRetry]:
// invokes fn until it succeeds (returning the value), the policy
// stops retrying, or ctx is done. Used by [Client.Subscribe] to retry
// the SSE handshake while carrying back the constructed
// [Subscription] on success. attempt starts at 0 for the first call.
func runWithRetryValue[T any](ctx context.Context, policy RetryPolicy, fn func(attempt int) (T, error)) (T, error) {
	var zero T
	attempt := 0
	for {
		started := time.Now()
		v, err := fn(attempt)
		if err == nil {
			return v, nil
		}
		elapsed := time.Since(started)

		delay, retry := policy.ShouldRetry(attempt, err)
		if !retry {
			return zero, err
		}
		// A retry is worth making only if the budget can pay for the wait
		// and for an attempt after it. Sleeping out a wait it cannot afford
		// spends the whole budget and then reports that nothing came back —
		// discarding the code, the request id and the hint naming when to
		// return, which is the only thing the caller can act on.
		//
		// The attempt that just failed is the only estimate available of what
		// the next one costs. It errs towards answering rather than timing
		// out: a retry that would have been quicker than its predecessor is
		// skipped, and the server's own words are returned instead.
		if deadline, bounded := ctx.Deadline(); bounded && time.Until(deadline) < delay+elapsed {
			return zero, err
		}
		// time.NewTimer + Stop (vs time.After) so that a ctx.Done
		// cancellation does not leave the timer's goroutine parked
		// until the full delay elapses.
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			// Both facts are true and the caller needs both: the error that
			// asked for the wait says what happened and when to come back,
			// and the context error says which side gave up. Returning the
			// context error alone says nothing came back when something did.
			return zero, errors.Join(err, ctx.Err())
		case <-t.C:
		}
		attempt++
	}
}
