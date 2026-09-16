package semantik

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransientRetry_HonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	var firstTime, secondTime time.Time
	srv, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		switch n {
		case 1:
			firstTime = time.Now()
			writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "", 50)
		default:
			secondTime = time.Now()
			writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_x"})
		}
	})
	_ = srv
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(2)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if waited := secondTime.Sub(firstTime); waited < 40*time.Millisecond {
		t.Errorf("retry waited %v, expected ~50ms", waited)
	}
}

func TestTransientRetry_StopsAtMax(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "", 5)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(2)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrBackpressure) {
		t.Fatalf("want ErrBackpressure, got %v", err)
	}
	if attempts.Load() != 3 { // initial + 2 retries
		t.Errorf("expected 3 total attempts, got %d", attempts.Load())
	}
}

func TestTransientRetry_RetriesUnavailable(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "reconfiguring", 40)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(2)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts (retry after unavailable), got %d", attempts.Load())
	}
}

// TestTransientRetry_RetriesMeteringUnavailable_BackoffSchedule
// exercises the fallback backoff table when the server omits a
// retry_after_ms hint. The expected gaps are the first two table
// entries: 100 ms then 2 s.
func TestTransientRetry_RetriesMeteringUnavailable_BackoffSchedule(t *testing.T) {
	var attempts atomic.Int32
	var times []time.Time
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		n := attempts.Add(1)
		if n < 3 {
			// No retry_after_ms on the wire — the policy must fall
			// back to its own schedule.
			writeError(t, w, http.StatusServiceUnavailable, CodeMeteringUnavailable, "", 0)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(5)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
	if len(times) < 3 {
		t.Fatalf("expected 3 timestamps, got %d", len(times))
	}
	gap1 := times[1].Sub(times[0])
	gap2 := times[2].Sub(times[1])
	// Slack on the lower bound for CI scheduler jitter, generous
	// upper bound for system load. The point is to prove the table
	// drove the timing, not to micro-benchmark.
	if gap1 < 80*time.Millisecond || gap1 > 500*time.Millisecond {
		t.Errorf("expected first retry gap ~100ms, got %v", gap1)
	}
	if gap2 < 1900*time.Millisecond || gap2 > 2500*time.Millisecond {
		t.Errorf("expected second retry gap ~2s, got %v", gap2)
	}
}

func TestTransientRetry_DoesNotRetryNotBillable(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusPaymentRequired, CodeNotBillable, "add a payment method", 0)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(5)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrNotBillable) {
		t.Fatalf("want ErrNotBillable, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("not_billable is terminal; expected 1 attempt, got %d", attempts.Load())
	}
}

func TestTransientRetry_DoesNotRetryRateLimited(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusTooManyRequests, CodeRateLimited, "", 0)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry for rate_limited), got %d", attempts.Load())
	}
}

func TestTransientRetry_RespectsContext(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "", 50)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(10)))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Publish(ctx, PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrBackpressure) {
		t.Errorf("expected ctx deadline or backpressure after timeout, got %v", err)
	}
}

func TestTransientRetry_OneRetry_IsDefault(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "", 1)
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrBackpressure) {
		t.Fatalf("want ErrBackpressure, got %v", err)
	}
	// Default policy is TransientRetry(1) ⇒ 1 initial + 1 retry.
	if got := attempts.Load(); got != 2 {
		t.Errorf("default should retry once on backpressure; attempts = %d, want 2", got)
	}
}

// TestTransientBackoff_Schedule asserts the fallback table that the
// retry policy consults when the server omits retry_after_ms. The
// schedule is [100ms, 2s, 5s, 10s] saturating at 10s. Cross-SDK
// reference: noetive-sdk-rust/src/semantik/retry.rs.
func TestTransientBackoff_Schedule(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, 100 * time.Millisecond}, // negative clamps to first entry
		{0, 100 * time.Millisecond},
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 10 * time.Second},
		{4, 10 * time.Second},  // saturated
		{50, 10 * time.Second}, // far beyond schedule
	}
	for _, tc := range cases {
		if d := transientBackoff(tc.attempt); d != tc.want {
			t.Errorf("attempt=%d: got %v, want %v", tc.attempt, d, tc.want)
		}
	}
}

// fixedWait asks for a wait long enough that nothing but the context can end
// it, which is the condition the two tests below are about.
type fixedWait struct{ delay time.Duration }

func (w fixedWait) ShouldRetry(int, error) (time.Duration, bool) { return w.delay, true }

// A budget that cannot cover the wait the server asked for must come back with
// what the server said, not with a deadline. Sleeping out an unaffordable hint
// spends the whole budget and then reports that nothing arrived, discarding the
// code, the request id and the time to return — the only things the caller can
// act on.
func TestAWaitTheBudgetCannotAffordReturnsTheRefusalInstead(t *testing.T) {
	refusal := &Error{
		Code:       CodeUnavailable,
		Message:    "embedder unavailable",
		RequestID:  "req_unaffordable",
		RetryAfter: time.Hour,
		HTTPStatus: http.StatusServiceUnavailable,
	}
	attempts := 0

	// Live, but far shorter than the hour the refusal asks for.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := runWithRetryValue(ctx, fixedWait{delay: time.Hour}, func(int) (struct{}, error) {
		attempts++
		return struct{}{}, refusal
	})

	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("the refusal was discarded, leaving nothing to act on: %v", err)
	}
	if got.Code != CodeUnavailable || got.RequestID != "req_unaffordable" || got.RetryAfter != time.Hour {
		t.Errorf("the refusal arrived stripped: %+v", got)
	}
	// Declining the wait is the point: the budget is still unspent, so the
	// caller is told what happened rather than that time ran out.
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a refusal that did arrive was reported as the budget running out: %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected the unaffordable retry to be declined, got %d attempts", attempts)
	}
}

// A wait the budget could afford, ended early by the caller withdrawing. The
// refusal that asked for the wait still has to reach the caller: both facts are
// true, and each answers a different question — what the server said, and which
// side gave up.
func TestAnInterruptedWaitKeepsTheRefusalThatAskedForIt(t *testing.T) {
	refusal := &Error{
		Code:       CodeUnavailable,
		Message:    "embedder unavailable",
		RequestID:  "req_interrupted",
		RetryAfter: time.Hour,
		HTTPStatus: http.StatusServiceUnavailable,
	}

	// No deadline, so the wait is entered rather than declined as unaffordable.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, err := runWithRetryValue(ctx, fixedWait{delay: time.Hour}, func(int) (struct{}, error) {
		// Withdraw while the wait is in flight.
		cancel()
		return struct{}{}, refusal
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("the caller cannot tell which side gave up: %v", err)
	}
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("the refusal that caused the wait was discarded: %v", err)
	}
	if got.Code != CodeUnavailable || got.RequestID != "req_interrupted" || got.RetryAfter != time.Hour {
		t.Errorf("the refusal arrived stripped: %+v", got)
	}
}
