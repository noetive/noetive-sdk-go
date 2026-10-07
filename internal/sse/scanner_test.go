package sse

import (
	"errors"
	"strings"
	"testing"
)

func TestScanner_SingleFrame(t *testing.T) {
	input := "event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	f := s.Frame()
	if f.Event != "match" {
		t.Errorf("Event = %q", f.Event)
	}
	if f.Data != `{"message_id":"m1","score":0.9}` {
		t.Errorf("Data = %q", f.Data)
	}
	if s.Scan() {
		t.Error("expected Scan to return false at EOF")
	}
	if err := s.Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
}

func TestScanner_MultipleFrames(t *testing.T) {
	input := "event: subscribed\ndata: {\"subscription_id\":\"sub_1\"}\n\n" +
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.8}\n\n" +
		"event: match\ndata: {\"message_id\":\"m2\",\"score\":0.7}\n\n"
	s := NewScanner(strings.NewReader(input))
	var got []Frame
	for s.Scan() {
		got = append(got, s.Frame())
	}
	if err := s.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	if got[0].Event != "subscribed" || got[1].Event != "match" || got[2].Event != "match" {
		t.Errorf("events = %v", got)
	}
}

func TestScanner_IgnoresCommentLines(t *testing.T) {
	input := ": ping\n: keepalive\nevent: match\ndata: {}\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if s.Frame().Event != "match" {
		t.Errorf("Event = %q", s.Frame().Event)
	}
}

func TestScanner_MultiLineData(t *testing.T) {
	// Per SSE spec, multiple "data:" lines in one frame are joined by '\n'.
	input := "event: match\ndata: line1\ndata: line2\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if got := s.Frame().Data; got != "line1\nline2" {
		t.Errorf("Data = %q", got)
	}
}

func TestScanner_CRLFLineEndings(t *testing.T) {
	input := "event: match\r\ndata: {}\r\n\r\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if s.Frame().Event != "match" {
		t.Errorf("Event = %q", s.Frame().Event)
	}
}

func TestScanner_UnknownFieldIgnored(t *testing.T) {
	input := "event: match\nid: 42\nretry: 1000\ndata: {}\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if got := s.Frame().Data; got != "{}" {
		t.Errorf("Data = %q", got)
	}
}

func TestScanner_EmptyFramesSkipped(t *testing.T) {
	input := "\n\n\nevent: match\ndata: {}\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if s.Frame().Event != "match" {
		t.Errorf("first real frame should be match, got %q", s.Frame().Event)
	}
}

func TestScanner_FrameTooLarge(t *testing.T) {
	big := "event: match\ndata: " + strings.Repeat("a", MaxFrameBytes+1) + "\n\n"
	s := NewScanner(strings.NewReader(big))
	if s.Scan() {
		t.Fatal("expected Scan to return false for oversized frame")
	}
	if !errors.Is(s.Err(), ErrFrameTooLarge) {
		t.Errorf("want ErrFrameTooLarge, got %v", s.Err())
	}
}

func TestScanner_SpaceAfterColonStrippedOnce(t *testing.T) {
	// Per SSE spec only the single leading space is stripped.
	input := "event: match\ndata:  two-spaces\n\n"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if got := s.Frame().Data; got != " two-spaces" {
		t.Errorf("Data = %q, want leading space preserved", got)
	}
}

func TestScanner_NoTrailingNewlineAtEOF(t *testing.T) {
	// A frame without the trailing blank line at EOF should still be emitted.
	input := "event: match\ndata: {}"
	s := NewScanner(strings.NewReader(input))
	if !s.Scan() {
		t.Fatalf("Scan failed: %v", s.Err())
	}
	if s.Frame().Event != "match" {
		t.Errorf("Event = %q", s.Frame().Event)
	}
}

// TestScanner_LimitIsTheCallers: a stream that batches many events needs a
// bound above the default, and the bound it chose is the one enforced — both
// for a frame on one long line and for one spread over many data lines.
func TestScanner_LimitIsTheCallers(t *testing.T) {
	const limit = 4 * MaxFrameBytes
	for name, frame := range map[string]func(n int) string{
		"one line": func(n int) string { return "event: batch\ndata: " + strings.Repeat("x", n) + "\n\n" },
		"many lines": func(n int) string {
			return "event: batch\n" + strings.Repeat("data: "+strings.Repeat("x", 1000)+"\n", n/1000) + "\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			if s := NewScannerLimit(strings.NewReader(frame(2*MaxFrameBytes)), limit); !s.Scan() {
				t.Errorf("a frame under the caller's bound was refused: %v", s.Err())
			}
			s := NewScannerLimit(strings.NewReader(frame(limit+1000)), limit)
			if s.Scan() || !errors.Is(s.Err(), ErrFrameTooLarge) {
				t.Errorf("a frame over the caller's bound was not refused: %v", s.Err())
			}
		})
	}
}
