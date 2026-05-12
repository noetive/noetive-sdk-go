package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// FuzzScanner asserts the SSE scanner never panics or allocates
// unboundedly on arbitrary input. Properties:
//
//   - Always terminates with either a clean EOF or ErrFrameTooLarge.
//   - Frame count is bounded by input size: the cheapest non-empty
//     frame is "\n\n" (two bytes), so n must be ≤ len(input)/2 + 1.
//   - Total decoded Event+Data bytes never exceed the input size.
//     A scanner that synthesised content out of thin air would be
//     a memory-amplification bug.
func FuzzScanner(f *testing.F) {
	seeds := []string{
		"",
		"\n",
		"\r\n",
		"event: match\ndata: {}\n\n",
		"event: match\ndata: {broken json\n\n",
		": comment only\n\n",
		"event: match\n",
		"event: \ndata: \n\n",
		"event: match\ndata: line1\ndata: line2\n\n",
		"\x00\x01\x02",
		"event: match\r\ndata: {}\r\n\r\n",
		"event: subscribed\ndata: {\"subscription_id\":\"sub_1\"}\n\n",
		strings.Repeat("event: match\ndata: {}\n\n", 1000),
		// Many short empty frames — exercises the per-frame upper
		// bound from the opposite direction.
		strings.Repeat("\n\n", 1000),
		// Multi-byte UTF-8 mid-frame (BOM + emoji).
		"\xef\xbb\xbfevent: match\ndata: \xf0\x9f\x98\x80\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Cap reader at 2 MiB so we don't spend fuzz time on absurdly
		// long inputs; the goal is input-shape robustness, not
		// throughput.
		const readCap = 2 << 20
		input := data
		if len(input) > readCap {
			input = input[:readCap]
		}
		r := io.LimitReader(strings.NewReader(string(data)), readCap)
		s := NewScanner(r)
		// Tightest possible frame is "\n\n" (2 bytes), so frame count
		// is bounded by len(input)/2 + 1. The +1 covers the case where
		// the trailing frame is unterminated and surfaces on EOF.
		maxFrames := len(input)/2 + 1
		var n, totalBytes int
		for s.Scan() {
			fr := s.Frame()
			totalBytes += len(fr.Event) + len(fr.Data)
			n++
			if n > maxFrames {
				t.Fatalf("frame count %d exceeds bound %d for input len %d",
					n, maxFrames, len(input))
			}
		}
		if totalBytes > len(input) {
			t.Errorf("decoded bytes %d exceed input length %d", totalBytes, len(input))
		}
		if err := s.Err(); err != nil && !errors.Is(err, ErrFrameTooLarge) {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
