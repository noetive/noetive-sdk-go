package bud

import (
	"context"
	"os"
	"time"
)

// Retries, and why there are almost none.
//
// A write changes the world. `send` is safe to re-issue only with an idempotency
// key the caller chose. `message.update` is safe to repeat as it was, but a
// repeat can overwrite labels another agent wrote in between, so it goes through
// the same gate rather than a second rule. This package cannot invent a key: a key it generated would differ across a process restart, so a genuine
// retry would send a second copy while two distinct calls would be collapsed into
// one. Both failures are worse than the error the caller would have seen.
//
// So the rule is narrow and mechanical:
//
//   - A refusal is never retried. It is an answer, not a failure — and the
//     envelope already says what to do instead.
//   - A Wait whose window closed empty is never retried. It is a successful call.
//   - A request that does not write is retried once on a connection that failed
//     before any response byte arrived.
//   - A request that writes is retried only when it carried an idempotency key.
//
// The last one is a gate in code rather than a line in a comment, because
// WithRetry must not be able to widen it. A caller can turn retries off or tune
// the schedule; it cannot make an unkeyed send repeatable.

// RetryPolicy decides whether to re-issue a request.
type RetryPolicy interface {
	// ShouldRetry reports whether attempt may be repeated. op is the operation
	// path and in is the request, so a policy can see whether a write carried a
	// key.
	ShouldRetry(attempt int, op string, in any) bool

	// Wait blocks before the next attempt, or returns the context's error.
	Wait(ctx context.Context, attempt int) error
}

// NoRetry issues every request exactly once.
type NoRetry struct{}

func (NoRetry) ShouldRetry(int, string, any) bool { return false }
func (NoRetry) Wait(context.Context, int) error   { return nil }

// TransientRetry re-issues a request at most Attempts times.
//
// Only a connection that failed reaches a policy at all — this package hands a
// decoded refusal back as a value, so a policy never sees one and cannot retry it.
type TransientRetry struct {
	// Attempts is how many retries, not how many tries. Zero is NoRetry.
	Attempts int

	// Backoff is the pause before each retry. A short schedule, because the only
	// thing being retried is a connection that did not open.
	Backoff []time.Duration
}

func defaultRetry() RetryPolicy { return TransientRetry{Attempts: 1} }

// ShouldRetry applies the idempotency gate.
func (p TransientRetry) ShouldRetry(attempt int, op string, in any) bool {
	if attempt >= p.Attempts {
		return false
	}
	if !writes(op) {
		return true
	}
	// A write without a key the caller chose is not repeatable, whatever policy
	// is installed. This is the check WithRetry cannot remove.
	return idempotencyKeyOf(in) != ""
}

func (p TransientRetry) Wait(ctx context.Context, attempt int) error {
	schedule := p.Backoff
	if len(schedule) == 0 {
		schedule = []time.Duration{100 * time.Millisecond, time.Second, 3 * time.Second}
	}
	d := schedule[min(attempt, len(schedule)-1)]

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// writes reports whether an operation changes anything.
//
// By name, from the closed set this package calls. A default of "it writes" is the
// safe direction: an operation nobody has classified is treated as unrepeatable,
// so adding one cannot quietly make it retryable.
func writes(op string) bool {
	switch op {
	case "me.describe", "health", "catalog.describe",
		"folder.list", "message.describe", "thread.describe", "part.describe",
		"mailbox.describe", "correspondent.list", "help.describe", "watch":
		return false
	default:
		return true
	}
}

// idempotencyKeyOf reads the key off whichever request this is.
//
// A type switch over the request types rather than reflection: the set is closed,
// and a switch is the spelling that fails to compile when a new request type
// forgets the field rather than silently reporting it absent.
func idempotencyKeyOf(in any) string {
	switch v := in.(type) {
	case SendInput:
		return v.IdempotencyKey
	case Change:
		return v.IdempotencyKey
	default:
		return ""
	}
}

// getenv is os.Getenv, named so the environment surface is greppable in one place.
func getenv(key string) string { return os.Getenv(key) }
