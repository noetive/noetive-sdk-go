// Command health performs an unauthenticated liveness probe against
// the Semantik endpoint. Exits 0 on 200, 1 on any other response or
// transport failure.
//
// Usage:
//
//	go run ./examples/health                  # defaults to production
//	NOETIVE_BASE_URL=... go run ./examples/health
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"
)

func main() {
	// Health is unauthenticated, so any syntactically valid key works.
	// NewFromEnv is not used because it requires NOETIVE_KEY_SECRET to be
	// set — which would be noise for this particular probe.
	key := os.Getenv("NOETIVE_KEY_SECRET")
	if key == "" {
		key = "keyu_placeholder_for_health_only"
	}
	opts := []semantik.Option{}
	if base := os.Getenv("NOETIVE_BASE_URL"); base != "" {
		opts = append(opts, semantik.WithBaseURL(base))
	}

	c, err := semantik.New(key, opts...)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Health(ctx); err != nil {
		fmt.Printf("DOWN: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("UP")
}
