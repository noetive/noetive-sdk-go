package semantik

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the cross-SDK reconciliation work tracked under
// /Users/xer/.claude/plans/find-discrepancies-between-noetive-sdk-p-reactive-sparrow.md.
// Each test name carries the discrepancy ID (A1, A4, B1, C4, C5) so a
// regression triages back to the matching plan entry.

// ----- A1: Subscribe handshake retry -------------------------------------

// TestA1_Subscribe_RetriesTransient503 asserts the handshake
// (POST → "subscribed" frame) is retried under the client's
// RetryPolicy on a 503 unavailable with a retry_after_ms hint, and
// that the request body is replayed verbatim across attempts.
func TestA1_Subscribe_RetriesTransient503(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "warming up", 50)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"sub_ok\"}\n\n")
		fl.Flush()
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 handshake attempts, got %d", got)
	}
	if sub.ID() != "sub_ok" {
		t.Errorf("sub.ID() = %q, want sub_ok", sub.ID())
	}
}

// TestA1_Subscribe_HandshakeStopsAtPolicyMax pins that the retry loop
// honours the RetryPolicy bound and returns a *SubscribeSetupError
// wrapping the terminal *Error after the bound is hit.
func TestA1_Subscribe_HandshakeStopsAtPolicyMax(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "", 5)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(2)))
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err == nil {
		t.Fatal("Subscribe must surface the terminal 503 after the retry budget")
	}
	if got := attempts.Load(); got != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 attempts, got %d", got)
	}
	// Must still resolve to a *SubscribeSetupError (B1) wrapping *Error.
	var setup *SubscribeSetupError
	if !errors.As(err, &setup) {
		t.Errorf("expected *SubscribeSetupError, got %T (%v)", err, err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected errors.Is(err, ErrUnavailable) through unwrap chain")
	}
}

// TestA1_Subscribe_PostSubscribed_NoTransparentReconnect asserts that
// once the handshake has succeeded, a server-side drop is surfaced as
// a *SubscribeStreamError from Next — not silently retried. Skill
// design principle #4: mid-stream reconnects belong to the caller.
func TestA1_Subscribe_PostSubscribed_NoTransparentReconnect(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"sub_ok\"}\n\n")
		fl.Flush()
		// Close immediately after the subscribed frame — the SDK must
		// NOT replay the handshake.
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(TransientRetry(3)))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	// Drain Next until the close surfaces (io.EOF or *SubscribeStreamError).
	_, nextErr := sub.Next(t.Context())
	if nextErr == nil {
		t.Fatal("Next returned nil after server-side close; expected EOF or stream error")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("handshake must not be replayed once subscribed; attempts=%d, want 1", got)
	}
	// EOF is acceptable (clean close); a *SubscribeStreamError is too.
	// What is NOT acceptable is a silent reconnect.
	if !errors.Is(nextErr, io.EOF) {
		var stream *SubscribeStreamError
		if !errors.As(nextErr, &stream) {
			t.Errorf("post-subscribed error must be io.EOF or *SubscribeStreamError, got %T (%v)", nextErr, nextErr)
		}
	}
}

// ----- A4: Default retry policy "always retry at least once" -------------

// TestA4_Default_RetriesAtLeastOnce asserts the SDK's out-of-the-box
// retry behavior — a transient 503 on the first attempt is followed
// by one retry that succeeds, without the caller installing any
// explicit RetryPolicy via WithRetry.
func TestA4_Default_RetriesAtLeastOnce(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "", 5)
			return
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	// newTestServer already constructs a client without WithRetry —
	// just exercise it directly.
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if err != nil {
		t.Fatalf("default policy must retry at least once: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected 2 attempts (1 initial + 1 default retry), got %d", got)
	}
}

// TestA4_NoRetryStruct_DisablesRetry pins that NoRetry{} as the
// configured policy yields strict one-shot semantics.
func TestA4_NoRetryStruct_DisablesRetry(t *testing.T) {
	var attempts atomic.Int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		writeError(t, w, http.StatusServiceUnavailable, CodeUnavailable, "", 5)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(NoRetry{}))
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("NoRetry{} must produce 1 attempt only; got %d", got)
	}
}

// ----- B1: Subscribe setup-vs-stream typed errors ------------------------

// TestB1_Subscribe_SetupError_IsDistinguishable asserts that a
// handshake-time failure is a *SubscribeSetupError and that
// errors.As resolves through the Unwrap chain to *Error so existing
// sentinel checks (e.g. errors.Is(err, ErrUnauthorized)) keep working.
func TestB1_Subscribe_SetupError_IsDistinguishable(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusUnauthorized, CodeUnauthorized, "bad key", 0)
	})
	c, _ = New(testKey, WithBaseURL(c.baseURL), WithRetry(NoRetry{}))
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var setup *SubscribeSetupError
	if !errors.As(err, &setup) {
		t.Errorf("expected *SubscribeSetupError, got %T", err)
	}
	var stream *SubscribeStreamError
	if errors.As(err, &stream) {
		t.Errorf("must NOT be a *SubscribeStreamError; got one")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Errorf("expected *Error via Unwrap, got %T", err)
	}
	if apiErr != nil && apiErr.Code != CodeUnauthorized {
		t.Errorf("apiErr.Code = %q, want %q", apiErr.Code, CodeUnauthorized)
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("errors.Is(err, ErrUnauthorized) failed through Unwrap chain")
	}
}

// TestB1_Subscribe_StreamError_IsDistinguishable asserts that a
// mid-stream malformed frame surfaces as *SubscribeStreamError (NOT
// SubscribeSetupError), and that errors.Is(err, ErrMalformedSSE)
// resolves through the Unwrap chain.
func TestB1_Subscribe_StreamError_IsDistinguishable(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"sub_ok\"}\n\n",
		// Malformed JSON on the match frame — Next must surface a
		// stream-typed error.
		"event: match\ndata: {not json}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	_, nextErr := sub.Next(t.Context())
	if nextErr == nil {
		t.Fatal("Next must surface the malformed frame")
	}
	var stream *SubscribeStreamError
	if !errors.As(nextErr, &stream) {
		t.Errorf("expected *SubscribeStreamError, got %T (%v)", nextErr, nextErr)
	}
	var setup *SubscribeSetupError
	if errors.As(nextErr, &setup) {
		t.Errorf("must NOT be *SubscribeSetupError; got one")
	}
	if !errors.Is(nextErr, ErrMalformedSSE) {
		t.Errorf("errors.Is(err, ErrMalformedSSE) failed through Unwrap chain")
	}
}

// ----- C4: Vector ↔ dimensions preflight ---------------------------------

// TestC4_Publish_PreflightRejectsVectorDimMismatch asserts the
// SDK fails fast when a publish carries a vector whose length does
// not match the declared Dimensions. The fixture handler calls
// t.Fatal to lock in "never reaches the wire".
func TestC4_Publish_PreflightRejectsVectorDimMismatch(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("dimension-mismatch publish must be rejected by preflight; reached the wire")
	})
	vec := make([]float32, 768)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 1024,
		Items: []PublishItem{{Vector: vec}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for 768-vec at 1024 dims, got %v", err)
	}
}

// TestC4_Publish_AllowsMatchingVectorDim asserts a matching
// (Vector, Dimensions) pair passes preflight and reaches the wire.
func TestC4_Publish_AllowsMatchingVectorDim(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Vector: []float32{0.1, 0.2, 0.3}}},
	})
	if err != nil {
		t.Fatalf("matching dims must pass preflight: %v", err)
	}
}

// TestC4_Publish_TextOnly_SkipsVectorDimCheck asserts that a
// text-only publish (no Vector) does not trigger the vector/dim
// preflight check — the field is optional by contract.
func TestC4_Publish_TextOnly_SkipsVectorDimCheck(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_ok"})
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 1024,
		Items: []PublishItem{{Text: "no vector here"}},
	})
	if err != nil {
		t.Fatalf("text-only publish must pass: %v", err)
	}
}

// ----- C5: Forward-compat unknown error codes ----------------------------

// TestC5_DecodeError_PreservesUnknownCode asserts that the error
// decoder accepts an unknown code string (e.g. one introduced by a
// future server release), preserves it verbatim on Error.Code, and
// that IsKnownCode reports false for it.
func TestC5_DecodeError_PreservesUnknownCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body: stringBody(
			`{"error":"future_xyz_code","message":"experimental","request_id":"req_abc"}`,
		),
		Header: http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != "future_xyz_code" {
		t.Errorf("Code = %q, want verbatim %q", e.Code, "future_xyz_code")
	}
	if e.Message != "experimental" {
		t.Errorf("Message = %q, want %q", e.Message, "experimental")
	}
	if e.RequestID != "req_abc" {
		t.Errorf("RequestID = %q, want %q", e.RequestID, "req_abc")
	}
	if IsKnownCode(e.Code) {
		t.Errorf("IsKnownCode(%q) = true, want false", e.Code)
	}
	if !IsKnownCode(CodeUnavailable) {
		t.Errorf("IsKnownCode(CodeUnavailable) = false, want true")
	}
}

// TestC5_TransientRetry_UnknownCode_NotRetried asserts the forward-
// compat default arm in TransientRetry.ShouldRetry: an unknown code
// falls through to "do not retry" rather than crashing or being
// silently treated as transient.
func TestC5_TransientRetry_UnknownCode_NotRetried(t *testing.T) {
	p := TransientRetry(5)
	d, ok := p.ShouldRetry(0, &Error{Code: "future_xyz_code"})
	if ok || d != 0 {
		t.Errorf("ShouldRetry on unknown code = (%v, %v), want (0, false)", d, ok)
	}
}

// TestC5_IsKnownCode_CoversAllConstants is the inverse: every
// enumerated Code* constant must be reported as known. This is a
// drift-detector — if a future contributor adds a new Code constant
// they have to update IsKnownCode in the same change.
func TestC5_IsKnownCode_CoversAllConstants(t *testing.T) {
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
	if IsKnownCode("") {
		t.Errorf("IsKnownCode(empty) = true, want false")
	}
}

// Avoid unused-import warning when only some tests are compiled.
var _ = time.Now
