package semantik

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestError_IsMatchesEveryCodeSentinel is the sentinel-matching
// property test: for every exported code sentinel, an *Error carrying
// that code satisfies errors.Is against the sentinel, and NO other
// sentinel accidentally matches. The spot-check table in
// error_test.go only covers a handful of codes; this locks in the
// invariant across the full set so adding a new code without wiring
// its sentinel (or vice versa) is an immediate test failure.
func TestError_IsMatchesEveryCodeSentinel(t *testing.T) {
	all := []struct {
		name     string
		code     string
		sentinel *Error
	}{
		{"InvalidRequest", CodeInvalidRequest, ErrInvalidRequest},
		{"Unauthorized", CodeUnauthorized, ErrUnauthorized},
		{"NotBillable", CodeNotBillable, ErrNotBillable},
		{"MethodNotAllowed", CodeMethodNotAllowed, ErrMethodNotAllowed},
		{"UnsupportedMediaType", CodeUnsupportedMediaType, ErrUnsupportedMediaType},
		{"RequestTooLarge", CodeRequestTooLarge, ErrRequestTooLarge},
		{"RateLimited", CodeRateLimited, ErrRateLimited},
		{"TooManyRequests", CodeTooManyRequests, ErrTooManyRequests},
		{"Backpressure", CodeBackpressure, ErrBackpressure},
		{"Unavailable", CodeUnavailable, ErrUnavailable},
		{"MeteringUnavailable", CodeMeteringUnavailable, ErrMeteringUnavailable},
		{"Internal", CodeInternalError, ErrInternal},
	}
	for _, c := range all {
		err := &Error{Code: c.code, HTTPStatus: 500, Message: "x"}
		if !errors.Is(err, c.sentinel) {
			t.Errorf("errors.Is({%s}, %s) = false", c.code, c.name)
		}
		// Cross-check: every other sentinel must NOT match.
		for _, other := range all {
			if other.code == c.code {
				continue
			}
			if errors.Is(err, other.sentinel) {
				t.Errorf("errors.Is({%s}, %s) = true (should be false)", c.code, other.name)
			}
		}
	}
}

// TestSubscription_NextAfterCloseDoesNotHang enforces the Close
// contract: after Close() returns, a caller still parked in Next()
// (or one that calls Next() afterwards) must unblock with an error
// rather than silently waiting for a stream that will never deliver.
func TestSubscription_NextAfterCloseDoesNotHang(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		// Block server-side so the SDK's Next() must be unblocked by
		// Close, not by a server-side EOF.
		<-r.Context().Done()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sub.Next(t.Context())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Next after Close should have returned an error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next hung after Close")
	}
}

// TestSubscription_CloseUnblocksParkedNext asserts the cross-goroutine
// cancellation contract the godoc promises: Close may be called from
// any goroutine and will unblock a Next() that is currently blocked in
// a read. This is the core of the "single-reader + any-goroutine
// Close" lifetime model.
func TestSubscription_CloseUnblocksParkedNext(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sub.Next(t.Context())
		done <- err
	}()

	// Give Next a chance to park on the read.
	time.Sleep(50 * time.Millisecond)
	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("parked Next should have returned an error after Close, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Next within 2s")
	}
}

// TestTransientBackoff_StrictlyMonotonic is the mathematical property
// of the backoff schedule: gap(i+1) > gap(i) for every attempt before
// the schedule saturates, and gap(i+1) == gap(i) thereafter. Only
// "at least N ms" assertions can flake on slow CI without catching a
// bug where the growth gets accidentally flattened.
func TestTransientBackoff_StrictlyMonotonic(t *testing.T) {
	prev := transientBackoff(0)
	saturatedAt := -1
	for i := 1; i <= 20; i++ {
		cur := transientBackoff(i)
		switch {
		case cur > prev:
			if saturatedAt >= 0 {
				t.Errorf("attempt %d: backoff grew after saturating at attempt %d", i, saturatedAt)
			}
		case cur == prev:
			if saturatedAt < 0 {
				saturatedAt = i - 1
			}
		default:
			t.Errorf("attempt %d: backoff shrank from %v to %v", i, prev, cur)
		}
		prev = cur
	}
	if saturatedAt < 0 {
		t.Errorf("backoff never saturated within 20 attempts — the cap must engage")
	}
	last := transientBackoffSchedule[len(transientBackoffSchedule)-1]
	if got := transientBackoff(100); got != last {
		t.Errorf("transientBackoff(100) = %v, want saturated %v", got, last)
	}
}

// TestSubscription_IDStableAfterClose guards the documented promise
// that sub.ID() is usable for the life of the Subscription object,
// including after Close — callers often log the ID from a deferred
// function that runs after shutdown.
func TestSubscription_IDStableAfterClose(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"sub_stable\"}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	before := sub.ID()
	_ = sub.Close()
	if after := sub.ID(); after != before {
		t.Errorf("ID drifted across Close: before=%q after=%q", before, after)
	}
}

// TestSubscription_EventsAfterCloseStopsImmediately combines the
// previous two: iterating Events after Close must terminate without
// issuing extra yields.
func TestSubscription_EventsAfterCloseStopsImmediately(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = sub.Close()

	done := make(chan int, 1)
	go func() {
		count := 0
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		for _, err := range sub.Events(ctx) {
			count++
			if err != nil {
				break
			}
		}
		done <- count
	}()
	select {
	case count := <-done:
		// The yield-with-err path may fire exactly once (carrying the
		// closed-stream error); anything beyond that is a contract
		// violation.
		if count > 1 {
			t.Errorf("Events after Close yielded %d times, want at most 1", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Events did not terminate after Close within 2s")
	}
}
