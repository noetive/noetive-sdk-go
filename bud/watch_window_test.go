package bud

import (
	"testing"
	"time"
)

// TestTheWaitWindowIsBounded: the server holds a quiet interval for at most
// MaxWaitSeconds, so a longer window would only wait on keepalives, and a zero or
// negative one means the default rather than a Wait that never waits.
func TestTheWaitWindowIsBounded(t *testing.T) {
	t.Parallel()

	max := MaxWaitSeconds * time.Second
	for seconds, want := range map[int]time.Duration{
		-1:                 max,
		0:                  max,
		1:                  time.Second,
		MaxWaitSeconds - 1: max - time.Second,
		MaxWaitSeconds:     max,
		MaxWaitSeconds + 1: max,
	} {
		if got := waitWindow(seconds); got != want {
			t.Errorf("waitWindow(%d) = %v, want %v", seconds, got, want)
		}
	}
}
