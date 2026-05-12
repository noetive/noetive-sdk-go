package semantik

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestPublish_Text_HappyPath(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req PublishRequest
		readJSON(t, r, &req)
		if len(req.Items) != 1 || req.Items[0].Text != "hello" {
			t.Errorf("unexpected items: %+v", req.Items)
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_1", Epoch: 42, Seq: 7})
	})
	res, err := c.Publish(t.Context(), PublishRequest{
		Namespace:  "n",
		Model:      "text-embedding-3-small",
		Dimensions: 384,
		Items:      []PublishItem{{Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.MessageID != "msg_1" || res.Epoch != 42 || res.Seq != 7 {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestPublish_Vector_WithAck(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req PublishRequest
		readJSON(t, r, &req)
		if req.Ack != AckDurable {
			t.Errorf("Ack = %q, want durable", req.Ack)
		}
		if len(req.Items) != 1 || len(req.Items[0].Vector) != 3 {
			t.Errorf("bad items: %+v", req.Items)
		}
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg_2", Epoch: 1, Seq: 1})
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Vector: []float32{0.1, 0.2, 0.3}}},
		Ack:   AckDurable,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestPublish_PreflightRejectsEmptyItem(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for empty item, got %v", err)
	}
}

func TestPublish_PreflightVectorTooLarge(t *testing.T) {
	c, _ := New(testKey)
	big := make([]float32, MaxVectorDim+1)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Vector: big}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for oversized vector, got %v", err)
	}
}

func TestPublish_PreflightTextTooLarge(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: strings.Repeat("a", MaxTextBytes+1)}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for oversized text, got %v", err)
	}
}

func TestPublish_MetadataBoundaries(t *testing.T) {
	c, _ := New(testKey)
	ok := PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items:    []PublishItem{{Text: "x"}},
		Metadata: map[string]string{"author": "jdoe", "source": "arxiv"},
	}
	// No server; expect a non-preflight error (connection refused) but
	// NOT a validation error.
	_, err := c.Publish(t.Context(), ok)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.HTTPStatus == 0 {
		t.Fatalf("metadata should pass validation, got preflight error: %v", err)
	}

	tooMany := make(map[string]string, MaxMetadataKeys+1)
	for i := range MaxMetadataKeys + 1 {
		tooMany[string(rune('a'+i))] = "v"
	}
	bad := ok
	bad.Metadata = tooMany
	_, err = c.Publish(t.Context(), bad)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for too many metadata keys, got %v", err)
	}

	ctrlKey := map[string]string{"k\nbroken": "v"}
	bad = ok
	bad.Metadata = ctrlKey
	_, err = c.Publish(t.Context(), bad)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest for control-char metadata key, got %v", err)
	}
}

func TestPublish_Backpressure_WithRetryAfter(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusTooManyRequests, CodeBackpressure, "queue full", 100)
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Namespace: "n", Model: "m", Dimensions: 3,
		Items: []PublishItem{{Text: "x"}},
	})
	if !errors.Is(err, ErrBackpressure) {
		t.Fatalf("want ErrBackpressure, got %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatal("errors.As failed")
	}
	if apiErr.RetryAfter <= 0 {
		t.Errorf("RetryAfter not populated: %+v", apiErr)
	}
}

func TestPublish_DefaultsWhenAllUnset(t *testing.T) {
	var body PublishRequest
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &body)
		writeJSON(t, w, http.StatusOK, PublishResponse{MessageID: "msg"})
	})
	_, err := c.Publish(t.Context(), PublishRequest{
		Items: []PublishItem{{Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if body.Namespace != DefaultNamespace {
		t.Errorf("Namespace = %q, want %q", body.Namespace, DefaultNamespace)
	}
	if body.Model != DefaultModel {
		t.Errorf("Model = %q, want %q", body.Model, DefaultModel)
	}
	if body.Dimensions != DefaultDimensions {
		t.Errorf("Dimensions = %d, want %d", body.Dimensions, DefaultDimensions)
	}
}

func TestAckMode_Valid(t *testing.T) {
	ok := []AckMode{"", AckStored, AckDurable}
	for _, a := range ok {
		if !a.Valid() {
			t.Errorf("AckMode(%q).Valid() = false", a)
		}
	}
	bad := []AckMode{"sometimes", "foo", "STORED", "best_effort"}
	for _, a := range bad {
		if a.Valid() {
			t.Errorf("AckMode(%q).Valid() = true", a)
		}
	}
}
