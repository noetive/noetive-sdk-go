// Command lint validates a SemQL query and prints diagnostics and
// completion suggestions. The endpoint is unauthenticated, so no API
// key is required.
//
// Usage:
//
//	go run ./examples/lint
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"
)

func main() {
	// Lint does not require auth, but the SDK still needs any valid
	// key prefix to satisfy [semantik.New]. Reuse a placeholder.
	c, err := semantik.New("keyu_placeholder_for_lint_only")
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	query := `MATCH DISTANCE("climate change") WITHIN `
	res, err := c.Lint(ctx, semantik.LintRequest{
		Query:  query,
		Cursor: len(query),
	})
	if err != nil {
		log.Fatalf("lint: %v", err)
	}

	fmt.Printf("valid=%t normalized=%q\n", res.Valid, res.Normalized)
	for _, d := range res.Diagnostics {
		fmt.Printf("  [%s] %d:%d %s\n", d.Severity, d.Line, d.Col, d.Message)
	}
	for _, cmp := range res.Completions {
		fmt.Printf("  -> %-12s %s — %s\n", cmp.Kind, cmp.Label, cmp.Detail)
	}
}
