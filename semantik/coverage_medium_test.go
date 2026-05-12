package semantik

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

// TestError_MalformedResponse_ConventionLocked guards the documented
// convention for the two client-side error flavours that both carry
// the pre-flight look:
//
//   - preflight validation  → Code != CodeMalformedResponse, HTTPStatus == 0
//   - malformed 2xx decode  → Code == CodeMalformedResponse, HTTPStatus == (server's 2xx)
//
// Callers rely on this to tell "we rejected before sending" apart
// from "server responded but the body was garbage". Do not swap
// either invariant without updating the documentation.
func TestError_MalformedResponse_ConventionLocked(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>not-json</html>`))
	})
	_, err := c.Search(t.Context(), SearchRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 384,
	})
	if err == nil {
		t.Fatal("expected malformed-body error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if apiErr.Code != CodeMalformedResponse {
		t.Errorf("Code = %q, want %q", apiErr.Code, CodeMalformedResponse)
	}
	if apiErr.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d; malformed 2xx must preserve the server status (want 200)", apiErr.HTTPStatus)
	}

	// A preflight failure must not collide with this convention.
	c2, _ := New(testKey)
	_, err2 := c2.Search(t.Context(), SearchRequest{Namespace: "n", Model: "m", Dimensions: 3})
	if err2 == nil {
		t.Fatal("expected preflight error")
	}
	var pf *Error
	if !errors.As(err2, &pf) {
		t.Fatalf("expected *Error, got %v", err2)
	}
	if pf.Code == CodeMalformedResponse {
		t.Error("preflight error must NOT use CodeMalformedResponse")
	}
	if pf.HTTPStatus != 0 {
		t.Errorf("preflight HTTPStatus = %d, want 0", pf.HTTPStatus)
	}
}

// TestMalformedSSEError_StructuredAs asserts that a malformed SSE
// frame surfaces both as an errors.Is match against ErrMalformedSSE
// (the existing sentinel contract) AND as an errors.As target on
// *MalformedSSEError so callers can inspect the underlying parse
// cause structurally.
func TestMalformedSSEError_StructuredAs(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: match\ndata: {not-json\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	_, err = sub.Next(t.Context())
	if err == nil {
		t.Fatal("expected malformed frame error")
	}
	// Sentinel match (preserves existing contract).
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want errors.Is(ErrMalformedSSE); got %v", err)
	}
	// Structured extraction.
	var mse *MalformedSSEError
	if !errors.As(err, &mse) {
		t.Fatalf("want errors.As(*MalformedSSEError); got %v", err)
	}
	if mse.Err == nil {
		t.Error("MalformedSSEError.Err should carry the underlying cause")
	}
}

// TestMalformedSSEError_ErrorAndUnwrap covers the String-formatting
// and Unwrap paths on the concrete type so the sentinel and the
// underlying cause both remain discoverable.
func TestMalformedSSEError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("bad data")
	mse := &MalformedSSEError{Err: cause}

	if got := mse.Error(); got == "" {
		t.Error("Error() should never be empty")
	}
	if !errors.Is(mse.Unwrap(), cause) {
		t.Errorf("Unwrap should expose the underlying cause; got %v", mse.Unwrap())
	}
	// Nil cases: the zero value and nil receiver must not panic.
	var zero MalformedSSEError
	_ = zero.Error()
	var nilPtr *MalformedSSEError
	_ = nilPtr.Error()
	if nilPtr.Unwrap() != nil {
		t.Error("Unwrap on nil receiver should return nil")
	}
}

// TestHealth_SendsContentType asserts the Health request carries the
// JSON Content-Type header like every other endpoint, even though
// Health has no body. Symmetry matters for operators who filter
// on Content-Type in ingress / WAF rules.
func TestHealth_SendsContentType(t *testing.T) {
	var got string
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got != "application/json" {
		t.Errorf("Health Content-Type = %q, want application/json", got)
	}
}

// TestSubscribe_DisablesGzipOnStream asserts that Subscribe sends
// Accept-Encoding: identity so intermediaries cannot gzip-wrap the
// SSE stream. Compression on a line-delimited real-time protocol
// costs CPU for no bandwidth win and can delay frame delivery; the
// other POST endpoints (which benefit from gzip on large JSON)
// remain free to negotiate compression.
func TestSubscribe_DisablesGzipOnStream(t *testing.T) {
	var gotAcceptEnc string
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEnc = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = sub.Close()
	if gotAcceptEnc != "identity" {
		t.Errorf("Subscribe Accept-Encoding = %q, want \"identity\"", gotAcceptEnc)
	}
}

// TestMalformedSSEError_HandshakeRejection covers the Subscribe-time
// rejections (wrong content-type, non-subscribed event, missing/empty
// subscription_id). All three must be discoverable via both sentinel
// and struct.
func TestMalformedSSEError_HandshakeRejection(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"wrongContentType", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		}},
		{"wrongFirstEvent", sseHandler(t, []string{
			"event: match\ndata: {\"message_id\":\"m\"}\n\n",
		})},
		{"missingSubscriptionID", sseHandler(t, []string{
			"event: subscribed\ndata: {}\n\n",
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := newTestServer(t, tc.handler)
			_, err := c.Subscribe(t.Context(), SubscribeRequest{
				Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrMalformedSSE) {
				t.Errorf("want errors.Is(ErrMalformedSSE); got %v", err)
			}
			var mse *MalformedSSEError
			if !errors.As(err, &mse) {
				t.Errorf("want errors.As(*MalformedSSEError); got %v", err)
			}
		})
	}
}
