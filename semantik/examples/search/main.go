// Command search demonstrates a SemQL text-anchor search against
// the "global" namespace, naming the namespace, model and dimensions
// explicitly as every request must.
//
// Usage:
//
//	NOETIVE_KEY_SECRET=keyu_... go run ./examples/search
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"
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
	res, err := c.Search(ctx, semantik.SearchRequest{
		Query:      `MATCH DISTANCE("machine learning research") WITHIN 0.4 LIMIT 10`,
		Namespace:  "global",
		Model:      "Qwen3-Embedding-4B",
		Dimensions: 1024,
	})
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	for i, r := range res.Results {
		fmt.Printf("%2d  score=%.3f  id=%s  ns=%s\n", i, r.Score, r.MessageID, r.Namespace)
	}
}
