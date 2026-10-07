// Command custom_retry demonstrates how to override the SDK's default
// retry policy. Two recipes are shown:
//
//  1. Disable retries entirely with semantik.NoRetry{} — useful for
//     benchmark or audit harnesses that require strict one-shot
//     semantics, or when a caller-supplied outer loop already owns
//     the retry decision.
//
//  2. Widen retries to TransientRetry(n) — useful when the workload
//     tolerates extra latency in exchange for better survivorship of
//     short transient pushback (CodeBackpressure, CodeUnavailable,
//     CodeNamespaceUnavailable, CodeMeteringUnavailable).
//
// Pair retries with PublishRequest.IdempotencyKey on writes so a
// retried request that actually committed on the server's side is
// de-duplicated rather than producing a second copy.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keya_... go run ./examples/custom_retry
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go.noetive.io/noetive-sdk-go/semantik"
)

func main() {
	apiKey := "keya_demo_key_replace_with_a_real_one"

	// --- 1. Strict one-shot: disable retries -----------------------------
	//
	// semantik.NoRetry{} is a zero-allocation RetryPolicy; pass the
	// zero value of the struct directly via WithRetry.
	oneShot, err := semantik.New(apiKey, semantik.WithRetry(semantik.NoRetry{}))
	if err != nil {
		log.Fatalf("init one-shot client: %v", err)
	}
	demo(oneShot, "one-shot (NoRetry{})")

	// --- 2. Wider retries ------------------------------------------------
	//
	// TransientRetry(n) honours the server's retry_after_ms hint when
	// present and otherwise falls back to the SDK's built-in schedule
	// (100 ms / 2 s / 5 s / 10 s, capped at 10 s).
	wider, err := semantik.New(apiKey, semantik.WithRetry(semantik.TransientRetry(3)))
	if err != nil {
		log.Fatalf("init widened client: %v", err)
	}
	demo(wider, "widened (TransientRetry(3))")
}

// demo runs a minimal Search through the client and prints the
// outcome. The point is to illustrate the policy plumbing, not to
// exercise the API — any error from a placeholder key is expected.
func demo(c *semantik.Client, label string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Search(ctx, semantik.SearchRequest{
		Query:      `MATCH DISTANCE("hello world") WITHIN 0.5 LIMIT 1`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	switch {
	case err == nil:
		fmt.Printf("[%s] ok\n", label)
	case errors.Is(err, semantik.ErrUnauthorized):
		fmt.Printf("[%s] unauthorized (expected for placeholder key)\n", label)
	default:
		fmt.Printf("[%s] %v\n", label, err)
	}
}
