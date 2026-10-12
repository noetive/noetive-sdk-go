// Command subscribe opens an SSE match stream for a SemQL query
// against the "global" namespace and prints match events as they
// arrive. A reported gap is logged and the stream read on.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keya_... go run ./examples/subscribe
//
// The command runs until SIGINT.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"go.noetive.io/noetive-sdk-go/semantik"
)

func main() {
	c, err := semantik.NewFromEnv()
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Bound the initial subscribe handshake; the stream itself is
	// long-lived and uses the parent (signal-aware) context.
	subCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Namespace, Model and Dimensions are required on every request;
	// the SDK applies no defaults.
	sub, err := c.Subscribe(subCtx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("gpu shortage")`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	if err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	log.Printf("subscribed: %s", sub.ID())

	for {
		ev, err := sub.Next(ctx)
		var missed *semantik.MissedMatchesError
		if errors.As(err, &missed) {
			log.Printf("missed %d matches; search with the same query to catch up", missed.Count)
			continue
		}
		if err != nil {
			log.Printf("stream ended: %v", err)
			return
		}
		fmt.Printf("match: %s score=%.3f\n", ev.MessageID, ev.Score)
	}
}
