package semantik

import (
	"errors"
	"strings"
	"testing"
)

// Publish preflight branches that the happy-path tests skip. Each one
// wires through the public method so the coverage tool attributes the
// branch to publish.go rather than only to validate.go.

func TestPublish_Preflight_EmptyModel(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace:  "private-ns",
		Dimensions: 3,
		Items:      []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty Model on private namespace should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_BadDimensions(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 0,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Dimensions=0 should preflight-fail, got %v", err)
	}
	_, err = c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: MaxVectorDim + 1,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Dimensions>MaxVectorDim should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_ItemCount(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: nil,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero items should preflight-fail, got %v", err)
	}
	_, err = c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "a"}, {Text: "b"}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("two items should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_IdempotencyKeyTooLong(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items:          []PublishItem{{Text: "x"}},
		IdempotencyKey: strings.Repeat("k", MaxIdempotencyKeyLen+1),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized idempotency key should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_InvalidAckMode(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
		Ack:   AckMode("teleport"),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown AckMode should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_InvalidUTF8Text(t *testing.T) {
	c, _ := New(testKey)
	// 0xff is a lone continuation byte — invalid UTF-8 at the first byte.
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "bad\xffbytes"}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid UTF-8 in Text should preflight-fail, got %v", err)
	}
}

func TestPublish_Preflight_InvalidUTF8Metadata(t *testing.T) {
	c, _ := New(testKey)
	// Invalid UTF-8 in a metadata key.
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items:    []PublishItem{{Text: "x"}},
		Metadata: map[string]string{"k\xff": "v"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid UTF-8 metadata key should preflight-fail, got %v", err)
	}
	// Invalid UTF-8 in a metadata value.
	_, err = c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items:    []PublishItem{{Text: "x"}},
		Metadata: map[string]string{"k": "v\xff"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid UTF-8 metadata value should preflight-fail, got %v", err)
	}
}

// Search preflight: negative Limit was documented but not asserted.
func TestSearch_Preflight_NegativeLimit(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Search(t.Context(), SearchRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 384,
		Limit: -1,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative Limit should preflight-fail, got %v", err)
	}
}
