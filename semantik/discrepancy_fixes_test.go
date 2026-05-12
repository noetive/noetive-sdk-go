package semantik

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubscribe_HandshakeRetries_OnRetryableServerError exercises the
// A1 fix: the subscribe handshake is wrapped in the client's
// RetryPolicy and honours the server's retry_after_ms hint. The
// fixture serves 503 + retry_after_ms:50 twice, then a valid SSE
// stream with a "subscribed" frame.
func TestSubscribe_HandshakeRetries_OnRetryableServerError(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "warming up", 50)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"sub_after_retry\"}\n\n")
		fl.Flush()
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))

	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe (with retries): %v", err)
	}
	defer sub.Close()

	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 handshake attempts (2 retries), got %d", got)
	}
	if sub.ID() != "sub_after_retry" {
		t.Errorf("ID = %q, want sub_after_retry", sub.ID())
	}
}

// TestSubscribe_StreamErrorAfterSubscribed_NotRetried locks in the
// post-handshake half of A1: once the "subscribed" frame is observed,
// a mid-stream failure surfaces directly from Next as
// *SubscribeStreamError — the SDK MUST NOT silently re-establish the
// stream (which would drop matches between the old and new
// subscription_id).
func TestSubscribe_StreamErrorAfterSubscribed_NotRetried(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		// Subscribed OK, then a malformed match frame mid-stream.
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: match\ndata: {this-is-not-json}\n\n")
		fl.Flush()
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(5)))

	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	_, err = sub.Next(t.Context())
	if err == nil {
		t.Fatal("Next should have errored on malformed match frame")
	}
	var streamErr *SubscribeStreamError
	if !errors.As(err, &streamErr) {
		t.Errorf("want *SubscribeStreamError from Next, got %T: %v", err, err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("stream error must not trigger reconnect; got %d handshake attempts, want 1", got)
	}
}

// TestNew_DefaultRetryEnabled covers A4: with no WithRetry option, a
// fresh Client must transparently survive a single transient 503 and
// then succeed on the second attempt. This is the "always retry at
// least once" stance the public SDK contract promises.
func TestNew_DefaultRetryEnabled(t *testing.T) {
	var attempts atomic.Int32
	srv, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "blip", 10)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	_ = srv
	// Construct a client WITHOUT calling WithRetry — the default
	// policy alone must carry it through the single 503.
	c, err := New(testKey, WithBaseURL(c.baseURL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("default retry should cover a single 503; got %d attempts, want 2", got)
	}
}

// TestSubscribeErrors_TypedClassification covers B1: the typed
// SubscribeSetupError and SubscribeStreamError values let callers
// branch on the lifecycle phase while still resolving to the
// underlying *Error through Unwrap.
func TestSubscribeErrors_TypedClassification(t *testing.T) {
	// Setup-phase failure: 401 from the handshake.
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusUnauthorized, CodeUnauthorized, "bad key", 0)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(NoRetry{}))

	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	var setupErr *SubscribeSetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("want *SubscribeSetupError on auth failure, got %T: %v", err, err)
	}
	// errors.Is must still resolve to the underlying *Error sentinel.
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrapper must preserve errors.Is(ErrUnauthorized); got %v", err)
	}
	// And the stream-error type must NOT match.
	var streamErr *SubscribeStreamError
	if errors.As(err, &streamErr) {
		t.Errorf("handshake error must not classify as stream error")
	}

	// Stream-phase failure: subscribed OK, then a malformed match.
	_, c2 := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: match\ndata: not-json\n\n")
		fl.Flush()
	})
	sub, err := c2.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe (stream case): %v", err)
	}
	defer sub.Close()

	_, err = sub.Next(t.Context())
	if err == nil {
		t.Fatal("Next should have errored")
	}
	var streamErr2 *SubscribeStreamError
	if !errors.As(err, &streamErr2) {
		t.Errorf("want *SubscribeStreamError after subscribed, got %T: %v", err, err)
	}
	var setupErr2 *SubscribeSetupError
	if errors.As(err, &setupErr2) {
		t.Errorf("stream error must not classify as setup error")
	}
}

// TestTransientBackoff_ScheduleDelays_C1 measures observed delays
// across three forced retries and asserts they approximate
// [100ms, 2s, 5s]. The fixture omits retry_after_ms so the table
// drives the timing.
func TestTransientBackoff_ScheduleDelays_C1(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long retry timing test in -short")
	}
	var times []time.Time
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		n := attempts.Add(1)
		if n < 4 {
			writeError(t, w, http.StatusServiceUnavailable, CodeMeteringUnavailable, "", 0)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "ok"})
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(5)))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if attempts.Load() != 4 {
		t.Fatalf("expected 4 attempts (3 retries), got %d", attempts.Load())
	}
	if len(times) < 4 {
		t.Fatalf("expected 4 timestamps, got %d", len(times))
	}
	gaps := []time.Duration{
		times[1].Sub(times[0]),
		times[2].Sub(times[1]),
		times[3].Sub(times[2]),
	}
	wants := []time.Duration{100 * time.Millisecond, 2 * time.Second, 5 * time.Second}
	for i, want := range wants {
		// ±50ms tolerance on the small entry; widen to ±300ms on the
		// 2s/5s entries where OS scheduling can drift more.
		tol := 50 * time.Millisecond
		if want >= time.Second {
			tol = 300 * time.Millisecond
		}
		if gaps[i] < want-tol || gaps[i] > want+tol+(2*time.Second) {
			// Upper bound is generous to keep CI green under load —
			// the regression we care about is a delay that's too
			// short (skipping the schedule).
			t.Errorf("retry %d: gap=%v, want approx %v (±%v)", i, gaps[i], want, tol)
		}
	}
}

// TestPublish_VectorLengthMismatchRejected_C4 covers the preflight
// guard: a vector whose length disagrees with the declared Dimensions
// must be rejected by the SDK before any HTTP round trip.
func TestPublish_VectorLengthMismatchRejected_C4(t *testing.T) {
	var hits atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "should-not-reach"})
	})

	vec := make([]float32, 768)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace:  "n",
		Model:      "m",
		Dimensions: 1024,
		Items:      []PublishItem{{Vector: vec}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for vector/dimensions mismatch, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server should not be hit on preflight rejection; got %d hits", got)
	}

	// Sanity: matching length still works.
	matched := make([]float32, 1024)
	_, err = c.Publish(t.Context(), PublishRequest{
		Namespace:  "n",
		Model:      "m",
		Dimensions: 1024,
		Items:      []PublishItem{{Vector: matched}},
	})
	if err != nil {
		t.Errorf("matching vector length should pass preflight, got %v", err)
	}
}

// TestIsKnownCode_ForwardCompat covers C5: an unrecognised wire code
// must flow through verbatim on Error.Code and report false from
// IsKnownCode, while every documented Code* constant reports true.
func TestIsKnownCode_ForwardCompat(t *testing.T) {
	// Known codes — exhaustive list.
	known := []string{
		CodeInvalidRequest,
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
		CodeMalformedResponse,
	}
	for _, code := range known {
		if !IsKnownCode(code) {
			t.Errorf("IsKnownCode(%q) = false, want true", code)
		}
	}

	// Unknown future code.
	if IsKnownCode("future_xyz_code") {
		t.Error("IsKnownCode(\"future_xyz_code\") = true, want false")
	}
	if IsKnownCode("") {
		t.Error("IsKnownCode(\"\") = true, want false")
	}

	// Decoder must preserve the future code verbatim without crashing.
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       stringBody(`{"error":"future_xyz_code","message":"unknown"}`),
		Header:     http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != "future_xyz_code" {
		t.Errorf("decodeError dropped unknown code; got %q", e.Code)
	}
	if IsKnownCode(e.Code) {
		t.Error("decoded unknown code reported as known")
	}
	// And it must not silently retry: TransientRetry's switch must
	// fall through to the default arm.
	if _, ok := TransientRetry(3).ShouldRetry(0, e); ok {
		t.Error("unknown code must not be retried by TransientRetry")
	}
}

// TestSubscribe_ContextCanceled_DuringHandshakeBackoff verifies that a
// caller-cancelled context unblocks the retry loop promptly even when
// the policy is mid-backoff between handshake attempts.
func TestSubscribe_ContextCanceled_DuringHandshakeBackoff(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Always 503 with a long retry_after_ms so the policy sleeps.
		writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "", 5000)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(5)))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	_, err := c.Subscribe(ctx, SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err == nil {
		t.Fatal("Subscribe should have errored on context cancel")
	}
	// Either ctx.Err() or a wrapped subscribe error carrying it is
	// acceptable; what matters is the call returned promptly.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled in chain, got %v", err)
	}
}
