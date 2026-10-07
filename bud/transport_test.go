package bud

import (
	"net/http"
	"testing"
)

// TestTheDefaultTransportBoundsHeadersNotBodies: a whole-exchange Timeout would
// cut Wait's window and every stream, and a missing header bound would let a
// server that never answers hold a Wait handshake indefinitely. A behavioural
// check of either would take longer than the bound itself, so this pins the
// configuration.
func TestTheDefaultTransportBoundsHeadersNotBodies(t *testing.T) {
	t.Parallel()

	c := defaultHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("Timeout = %v; it would sever the stream", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout != DefaultResponseTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseTimeout)
	}
}
