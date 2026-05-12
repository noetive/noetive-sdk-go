package sse

import (
	"bytes"
	"strings"
	"testing"
)

// BenchmarkScannerThroughput measures the SSE scanner running over a
// steady-state match-frame stream. SetBytes lets go test report MB/s.
// The pattern matches what a busy /v1/subscribe channel looks like.
func BenchmarkScannerThroughput(b *testing.B) {
	frame := "event: match\ndata: {\"message_id\":\"msg_abcdef0123456789\",\"score\":0.82}\n\n"
	const frames = 256
	stream := strings.Repeat(frame, frames)
	src := []byte(stream)

	b.ReportAllocs()
	b.SetBytes(int64(len(src)))

	for b.Loop() {
		s := NewScanner(bytes.NewReader(src))
		n := 0
		for s.Scan() {
			f := s.Frame()
			_ = f.Event
			_ = f.Data
			n++
		}
		if n != frames {
			b.Fatalf("got %d frames, want %d", n, frames)
		}
		if err := s.Err(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScannerSteadyState reuses a single Scanner across
// iterations with a bytes.Reader reset — models the real subscription
// hot path (one scanner per subscription, many frames over its life).
func BenchmarkScannerSteadyState(b *testing.B) {
	frame := "event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n"
	src := []byte(frame)
	r := bytes.NewReader(src)
	s := NewScanner(r)
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		r.Reset(src)
		if !s.Scan() {
			b.Fatalf("scan: %v", s.Err())
		}
		_ = s.Frame()
	}
}
