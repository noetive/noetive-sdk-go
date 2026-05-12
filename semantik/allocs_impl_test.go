package semantik

import "testing"

// Split out so the main allocs_test.go does not shadow the stdlib
// symbol name.
func testingAllocsPerRunImpl(runs int, fn func()) float64 {
	return testing.AllocsPerRun(runs, fn)
}
