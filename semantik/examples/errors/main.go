// Command errors demonstrates the SDK's three error classes and the
// idiomatic ways to inspect them. The program intentionally triggers:
//
//  1. A client-side pre-flight rejection (over-sized vector),
//  2. An authenticated server response (either success or an API
//     error, depending on the key's state),
//
// and then shows how to distinguish each with errors.Is / errors.As.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keya_... go run ./examples/errors
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
	c, err := semantik.NewFromEnv()
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// --- 1. Pre-flight error (never reaches the wire) ---------------
	//
	// Oversized vector — the SDK rejects locally with HTTPStatus == 0.
	// An explicit Namespace/Model/Dimensions short-circuits the SDK's
	// defaults path and makes the failure condition obvious.
	_, err = c.Publish(ctx, semantik.PublishRequest{
		Namespace:  "private-x",
		Model:      "text-embedding-3-small",
		Dimensions: 5000,
		Items:      []semantik.PublishItem{{Vector: make([]float32, 5000)}},
	})
	classify("pre-flight oversized vector", err)

	// --- 2. Real round trip ----------------------------------------
	//
	// Minimal request against the global namespace, which every account has.
	_, err = c.Search(ctx, semantik.SearchRequest{
		Query:      `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 5`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	classify("search round trip", err)
}

// classify prints an error using every idiomatic detection pattern
// the SDK supports.
func classify(label string, err error) {
	fmt.Printf("\n--- %s ---\n", label)
	if err == nil {
		fmt.Println("ok")
		return
	}

	// Sentinel match: errors.Is walks the chain.
	switch {
	case errors.Is(err, semantik.ErrInvalidRequest):
		fmt.Println("matched: ErrInvalidRequest")
	case errors.Is(err, semantik.ErrUnauthorized):
		fmt.Println("matched: ErrUnauthorized")
	case errors.Is(err, semantik.ErrNotBillable):
		fmt.Println("matched: ErrNotBillable (terminal until billing resolved)")
	case errors.Is(err, semantik.ErrBackpressure):
		fmt.Println("matched: ErrBackpressure")
	case errors.Is(err, semantik.ErrUnavailable):
		fmt.Println("matched: ErrUnavailable")
	case errors.Is(err, semantik.ErrMeteringUnavailable):
		fmt.Println("matched: ErrMeteringUnavailable (retry with backoff)")
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Println("matched: context.DeadlineExceeded (not wrapped by SDK)")
	case errors.Is(err, context.Canceled):
		fmt.Println("matched: context.Canceled (not wrapped by SDK)")
	}

	// Full detail extraction.
	var apiErr *semantik.Error
	if errors.As(err, &apiErr) {
		source := "server"
		if apiErr.HTTPStatus == 0 {
			source = "pre-flight"
		}
		fmt.Printf("source=%s http=%d code=%s retry_after=%v message=%q\n",
			source, apiErr.HTTPStatus, apiErr.Code, apiErr.RetryAfter, apiErr.Message)
		return
	}

	// Anything else is a raw transport or context error.
	fmt.Printf("raw: %v\n", err)
}
