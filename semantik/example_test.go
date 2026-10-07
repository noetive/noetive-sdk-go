package semantik_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"go.noetive.io/noetive-sdk-go/semantik"
)

func ExampleNew() {
	c, err := semantik.New("keya_abc...")
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleNewFromEnv() {
	// Reads NOETIVE_KEY_SECRET (required) and NOETIVE_SEMANTIK_BASE_URL (optional).
	c, err := semantik.NewFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleClient_Health() {
	c, _ := semantik.New("keya_abc...")
	if err := c.Health(context.Background()); err != nil {
		log.Printf("semantik unhealthy: %v", err)
	}
}

func ExampleClient_Search() {
	c, _ := semantik.New("keya_abc...")
	res, err := c.Search(context.Background(), semantik.SearchRequest{
		Query:      `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 10`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range res.Results {
		fmt.Println(r.MessageID, r.Score)
	}
}

func ExampleClient_Publish() {
	c, _ := semantik.New("keya_abc...")
	res, err := c.Publish(context.Background(), semantik.PublishRequest{
		Items:          []semantik.PublishItem{{Text: "hello world"}},
		Namespace:      "global",
		Model:          "Qwen3-Embedding-4B",
		Dimensions:     1024,
		IdempotencyKey: "pub-2026-04-18-00001",
		Ack:            semantik.AckStored,
	})
	if err != nil {
		log.Fatal(err)
	}
	_ = res.MessageID
}

func ExampleClient_Lint() {
	c, _ := semantik.New("keya_abc...")
	res, err := c.Lint(context.Background(), semantik.LintRequest{
		Query: `MATCH DISTANCE("machin") WITHIN `,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		fmt.Printf("%s:%d:%d: %s\n", d.Severity, d.Line, d.Col, d.Message)
	}
	for _, cpl := range res.Completions {
		fmt.Println("suggest", cpl.Label)
	}
}

func ExampleClient_Subscribe() {
	c, _ := semantik.New("keya_abc...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := c.Subscribe(ctx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("breaking news") WITHIN 0.3`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	fmt.Println("subscription", sub.ID())

	for ev, err := range sub.Events(ctx) {
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("stream error: %v", err)
			}
			return
		}
		fmt.Println("match", ev.MessageID, ev.Score)
	}
}

func ExampleSubscription_Next() {
	c, _ := semantik.New("keya_abc...")
	sub, err := c.Subscribe(context.Background(), semantik.SubscribeRequest{Query: "..."})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	for {
		ev, err := sub.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			log.Printf("stream error: %v", err)
			return
		}
		fmt.Println(ev.MessageID, ev.Score)
	}
}

func ExampleSubscription_Events() {
	c, _ := semantik.New("keya_abc...")
	sub, err := c.Subscribe(context.Background(), semantik.SubscribeRequest{Query: "..."})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	for ev, err := range sub.Events(context.Background()) {
		if err != nil {
			break
		}
		fmt.Println(ev.MessageID)
	}
}

func ExampleSubscription_Close() {
	c, _ := semantik.New("keya_abc...")
	sub, err := c.Subscribe(context.Background(), semantik.SubscribeRequest{Query: "..."})
	if err != nil {
		log.Fatal(err)
	}
	// Close is idempotent and safe to call from any goroutine; a defer
	// guards every exit path, including early break out of Events.
	defer sub.Close()
}

func ExampleTransientRetry() {
	// The SDK rides out transient hiccups by default. Pass a lower
	// bound when the workload prefers to fail fast, NoRetry{} for
	// strict one-shot semantics, or a higher bound to keep trying.
	c, err := semantik.New("keya_abc...",
		semantik.WithRetry(semantik.TransientRetry(3)))
	if err != nil {
		log.Fatal(err)
	}
	// Pair retries with an idempotency key so a Publish that lands
	// after a retried response is not duplicated.
	_, _ = c.Publish(context.Background(), semantik.PublishRequest{
		Namespace:      "global",
		Model:          "Qwen3-Embedding-4B",
		Dimensions:     1024,
		Items:          []semantik.PublishItem{{Text: "retrying"}},
		IdempotencyKey: "pub-idk-1",
	})
}

func ExampleNoRetry() {
	// Disable the SDK's default single transient retry — every
	// request is exactly one round trip. Useful when the caller
	// wraps requests in its own retry loop or needs strict
	// one-shot semantics for a benchmark or audit harness.
	c, err := semantik.New("keya_abc...",
		semantik.WithRetry(semantik.NoRetry{}))
	if err != nil {
		log.Fatal(err)
	}
	_, _ = c.Search(context.Background(), semantik.SearchRequest{Query: "..."})
}

func ExampleError() {
	c, _ := semantik.New("keya_abc...")
	_, err := c.Search(context.Background(), semantik.SearchRequest{Query: "x"})
	switch {
	case errors.Is(err, context.Canceled):
		// caller gave up
	case err != nil:
		// Other errors carry a RequestID; quote it when contacting support.
		var e *semantik.Error
		if errors.As(err, &e) {
			log.Printf("search failed: %v (request_id=%s)", err, e.RequestID)
		} else {
			log.Printf("search failed: %v", err)
		}
	}
}
