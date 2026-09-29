// Command publish_vector publishes a pre-computed embedding vector
// to the "global" namespace with ack=durable. Pairing IdempotencyKey
// with durable ack gives exactly-once publish semantics that are safe
// to retry; the SDK's default retry rides out transient hiccups
// automatically.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keyu_... go run ./examples/publish_vector
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	// Caller-side vector must match the request's Dimensions, which in
	// turn must match the namespace's configured dimensionality.
	const dimensions = 1024
	vec := make([]float32, dimensions)
	for i := range vec {
		vec[i] = float32(i%100) * 0.01
	}

	res, err := c.Publish(ctx, semantik.PublishRequest{
		Items:          []semantik.PublishItem{{Vector: vec}},
		Namespace:      "global",
		Model:          "Qwen3-Embedding-4B",
		Dimensions:     dimensions,
		Ack:            semantik.AckDurable,
		IdempotencyKey: newKey(),
	})
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("message_id=%s epoch=%d seq=%d\n", res.MessageID, res.Epoch, res.Seq)
}

func newKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "pub-" + hex.EncodeToString(b[:])
}
