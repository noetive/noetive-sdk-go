package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestScanner_ScanAfterError locks in the sticky-error invariant:
// once Scan returns false due to an error, a subsequent Scan must
// keep returning false rather than clearing state and reading past
// the failure point.
func TestScanner_ScanAfterError(t *testing.T) {
	big := "event: match\ndata: " + strings.Repeat("a", MaxFrameBytes+1) + "\n\n" +
		"event: match\ndata: {}\n\n"
	s := NewScanner(strings.NewReader(big))
	if s.Scan() {
		t.Fatal("expected first Scan to fail on oversized frame")
	}
	if !errors.Is(s.Err(), ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge after first Scan, got %v", s.Err())
	}
	// Sticky: calling Scan again must still return false and must not
	// try to consume the next (valid) frame.
	if s.Scan() {
		t.Error("subsequent Scan should remain false after error")
	}
	if !errors.Is(s.Err(), ErrFrameTooLarge) {
		t.Errorf("Err should remain sticky; got %v", s.Err())
	}
}

// TestScanner_LoneCarriageReturn covers the bare-\r line terminator
// that some SSE producers emit. The splitSSELines function has to
// defer the decision until the next byte arrives to distinguish \r
// from \r\n; a naïve implementation would either drop a frame or
// double-split.
func TestScanner_LoneCarriageReturn(t *testing.T) {
	input := "event: match\rdata: {\"message_id\":\"m1\"}\r\r"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	f := s.Frame()
	if f.Event != "match" {
		t.Errorf("Event = %q, want match", f.Event)
	}
	if f.Data != `{"message_id":"m1"}` {
		t.Errorf("Data = %q", f.Data)
	}
}

// TestScanner_ReassemblyAcrossReads feeds the scanner one byte at a
// time through a drip reader. The bufio buffering must reassemble
// correctly, honour the lone-\r termination decision, and still emit
// the frame exactly once.
func TestScanner_ReassemblyAcrossReads(t *testing.T) {
	input := "event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n"
	s := NewScanner(&dripReader{data: []byte(input)})
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	f := s.Frame()
	if f.Event != "match" || f.Data != `{"message_id":"m1","score":0.9}` {
		t.Errorf("reassembly failed: %+v", f)
	}
}

// dripReader emits one byte per Read call so Scan must drive the
// underlying bufio.Reader through many partial reads before producing
// a single frame.
type dripReader struct {
	data []byte
	off  int
}

func (d *dripReader) Read(p []byte) (int, error) {
	if d.off >= len(d.data) {
		return 0, io.EOF
	}
	p[0] = d.data[d.off]
	d.off++
	return 1, nil
}
