package semantik

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
)

// TestWithHTTPClient_ReplacesTransport asserts that the documented
// option actually does what it says: requests route through the
// caller-supplied Doer instead of the default transport. Without this
// regression guard, a refactor could silently drop the injection.
func TestWithHTTPClient_ReplacesTransport(t *testing.T) {
	var calls atomic.Int32
	inj := &doerRecorder{calls: &calls, body: `{}`, status: http.StatusOK}

	c, err := New(testKey, WithHTTPClient(inj))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("injected Doer received %d calls, want 1", calls.Load())
	}
}

type doerRecorder struct {
	calls  *atomic.Int32
	body   string
	status int
}

func (d *doerRecorder) Do(_ *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// TestTransientRetry_ZeroOrNegativeMax_IsNoRetry locks in the godoc
// promise that TransientRetry(n) with n <= 0 disables retry. The
// behaviour must match NoRetry() exactly so callers can safely use
// a configured-via-flag value without a runtime guard.
func TestTransientRetry_ZeroOrNegativeMax_IsNoRetry(t *testing.T) {
	for _, max := range []int{0, -1, -100} {
		p := TransientRetry(max)
		d, ok := p.ShouldRetry(0, &Error{Code: CodeBackpressure, RetryAfter: 1})
		if ok || d != 0 {
			t.Errorf("TransientRetry(%d).ShouldRetry = (%v,%v), want (0,false)", max, d, ok)
		}
	}
}

// TestTransientRetry_NonAPIError_NotRetried asserts that a transport
// error (*url.Error, context errors, stdlib io errors) never satisfies
// TransientRetry: only the typed *Error codes listed in the godoc are
// eligible.
func TestTransientRetry_NonAPIError_NotRetried(t *testing.T) {
	p := TransientRetry(5)
	nonAPI := []error{
		errors.New("plain"),
		context.Canceled,
		context.DeadlineExceeded,
		io.ErrUnexpectedEOF,
	}
	for _, e := range nonAPI {
		if d, ok := p.ShouldRetry(0, e); ok || d != 0 {
			t.Errorf("ShouldRetry(%v) = (%v,%v), want (0,false)", e, d, ok)
		}
	}
}

// TestEncodeJSON_PanicRecovery drives the defer-recover in encodeJSON.
// A MarshalJSON implementation that panics is the cleanest stand-in
// for the "exotic struct shape" defence the recover was added for. A
// future goccy/go-json refactor could trip checkptr in a hotter path;
// this test keeps the safety-net branch alive.
func TestEncodeJSON_PanicRecovery(t *testing.T) {
	_, _, err := encodeJSON(panickingMarshaler{})
	if err == nil {
		t.Fatal("expected error from encode panic")
	}
	if !strings.Contains(err.Error(), "encode panic") {
		t.Errorf("error should mention encode panic, got %q", err.Error())
	}
}

type panickingMarshaler struct{}

func (panickingMarshaler) MarshalJSON() ([]byte, error) { panic("boom") }

// TestEncodeJSON_ErrorReturn exercises the clean-error path (no panic;
// goccy/go-json returns an error for types it cannot marshal). Paired
// with the panic test above, this covers both failure modes in
// encodeJSON.
func TestEncodeJSON_ErrorReturn(t *testing.T) {
	// A chan value is one of the few types goccy reliably refuses to
	// marshal without panicking.
	type bad struct{ Ch chan int }
	_, _, err := encodeJSON(bad{Ch: make(chan int)})
	if err == nil {
		t.Fatal("expected encode error for chan field")
	}
}

// TestSafeDecode_IOError confirms that a reader returning an error
// surfaces through safeDecode rather than being swallowed or converted
// into a spurious JSON parse failure.
func TestSafeDecode_IOError(t *testing.T) {
	sentinel := errors.New("read blew up")
	r := iotest.ErrReader(sentinel)
	var v any
	err := safeDecode(r, &v)
	if !errors.Is(err, sentinel) {
		t.Errorf("safeDecode should propagate reader errors; got %v", err)
	}
}

// TestDecodeError_MalformedBodyTruncated drives the >256-byte
// diagnostic-excerpt branch. The Error's Message must contain the
// truncation-indicator prefix and must NOT embed the full body —
// otherwise a hostile server could pad multi-megabyte text into a
// client's structured logs.
func TestDecodeError_MalformedBodyTruncated(t *testing.T) {
	big := strings.Repeat("A", 2048) // well over the 256-byte cap
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       stringBody(big),
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code == "" {
		t.Fatal("Code should be populated")
	}
	if len(e.Message) >= len(big) {
		t.Errorf("Message (%d bytes) should be truncated below body size (%d)", len(e.Message), len(big))
	}
	if !strings.Contains(e.Message, "malformed body") {
		t.Errorf("Message should carry the truncation-indicator prefix, got %q", e.Message)
	}
	if !strings.Contains(e.Message, fmt.Sprintf("%d bytes", len(big))) {
		t.Errorf("Message should report original body size, got %q", e.Message)
	}
}
