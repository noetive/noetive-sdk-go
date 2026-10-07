// Command publish_text publishes a text message to the "global"
// namespace; the server computes the embedding. Uses
// ack=stored: returns once the message is acknowledged across
// redundant copies and survives a single server restart. Use
// ack=durable for survivorship across power loss.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keya_... go run ./examples/publish_text
package main

import (
	"context"
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

	// Namespace, Model and Dimensions are required on every request;
	// the SDK applies no defaults.
	res, err := c.Publish(ctx, semantik.PublishRequest{
		Items: []semantik.PublishItem{{
			Text: "Transformer models have reshaped NLP benchmarks.",
		}},
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
		Metadata: map[string]string{
			"source": "arxiv",
			"author": "jdoe",
		},
		Ack: semantik.AckStored,
	})
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("message_id=%s epoch=%d seq=%d\n", res.MessageID, res.Epoch, res.Seq)
}
