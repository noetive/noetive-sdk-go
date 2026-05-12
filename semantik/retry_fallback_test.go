package semantik

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestTransientRetry_BackpressureWithoutHint asserts that a
// CodeBackpressure response without a RetryAfter hint still gets
// retried under TransientRetry. The README promises "CodeBackpressure
// (HTTP 429) — service is applying backpressure" is "safe to re-issue";
// silently going terminal when the server omits the hint violates that
// contract.
func TestTransientRetry_BackpressureWithoutHint(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "no hint", 0)
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
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 attempts (2 retries on hint-less backpressure), got %d", got)
	}
}

// TestTransientRetry_UnavailableWithoutHint mirrors the backpressure
// case for CodeUnavailable: the README lists it as "safe to re-issue",
// so a missing hint must not turn it terminal.
func TestTransientRetry_UnavailableWithoutHint(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 2 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "", 0)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
}

// TestDecodeError_RetryAfterMs_Capped asserts that an attacker-controlled
// retry_after_ms (up to uint32 max) cannot translate into a multi-week
// sleep. The header path already caps at maxRetryAfterSecs; the body
// path must match. A malicious or misconfigured server emitting the
// largest possible uint32 (~49 days in ms) would otherwise park a
// retrying caller for that long.
func TestDecodeError_RetryAfterMs_Capped(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       stringBody(`{"error":"backpressure","retry_after_ms":4294967295}`),
		Header:     http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	// A cap of roughly maxRetryAfterSecs (~292 years) is fine — the
	// point is to reject anything that could overflow int64 nanoseconds
	// or park a caller for days. We specifically check the uint32 max
	// case is not turned into a naive multi-day duration.
	if e.RetryAfter > 24*time.Hour {
		t.Errorf("body retry_after_ms=uint32 max should be capped or rejected, got %v", e.RetryAfter)
	}
}

// TestDecodeError_RateLimited_IsNotBackpressure locks in the policy
// that a 429 with an empty or malformed body maps to CodeRateLimited
// (never retried) rather than CodeBackpressure (retried). Blind
// retries of a rate-limited response can get the caller blocked
// harder; keeping the distinction explicit prevents a well-meaning
// future contributor from "fixing" the fallback.
func TestDecodeError_RateLimited_IsNotBackpressure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       stringBody(""),
		Header:     http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if !errors.Is(e, ErrRateLimited) {
		t.Errorf("want ErrRateLimited fallback for empty-body 429, got %v", e)
	}
	if errors.Is(e, ErrBackpressure) {
		t.Errorf("empty-body 429 must NOT map to CodeBackpressure (blind retry is harmful)")
	}
}

// TestTransientRetry_RateLimited_NotRetriedOnEmptyBody reinforces the
// same invariant at the policy level: TransientRetry must treat
// empty-body 429 as terminal.
func TestTransientRetry_RateLimited_NotRetriedOnEmptyBody(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// Empty 429 — no body, no Retry-After header.
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("empty-body 429 must not retry; got %d attempts", got)
	}
}
