package semantik

import "context"

// AckMode selects the durability level for a publish. Zero value is
// equivalent to AckStored on the wire — the server treats missing and
// "stored" identically.
type AckMode string

// Acknowledgement durability levels.
const (
	AckStored  AckMode = "stored"
	AckDurable AckMode = "durable"
)

// Valid reports whether a is a known AckMode value. The zero value
// (AckMode("")) is considered valid because the server interprets it
// as stored.
func (a AckMode) Valid() bool {
	switch a {
	case "", AckStored, AckDurable:
		return true
	default:
		return false
	}
}

// PublishItem is a single message to publish. At least one of Text or
// Vector must be non-zero. When both are provided, Vector takes
// precedence: the server stores the supplied vector as-is and does
// not embed Text. This lets callers that already have an embedding
// include the source text in the request without paying for a
// server-side embed call.
//
// The SDK does not copy Vector; callers must not mutate the slice
// until Publish returns.
//
// Field ordering: slice (24 B) > string (16 B).
type PublishItem struct {
	Vector []float32 `json:"vector,omitempty"`
	Text   string    `json:"text,omitempty"`
}

// PublishRequest is the body of POST /v1/publish.
//
// Defaults apply when targeting the global configuration:
//
//   - Namespace empty ⇒ [DefaultNamespace] ("global")
//   - Model empty + namespace is global ⇒ [DefaultModel]
//   - Dimensions zero + namespace is global ⇒ [DefaultDimensions]
//
// A minimal text publish is therefore
// PublishRequest{Items: []PublishItem{{Text: "..."}}}.
// Private namespaces require dashboard configuration and incur usage
// charges; callers using one MUST set Model and Dimensions explicitly.
//
// Dimensions must match the embedding Model and, when publishing a
// vector, the length of the vector itself. A mismatch is a
// server-side error.
//
// Items must currently contain exactly one element; the slice type
// reflects the wire schema and is a forward-compatible shape for
// future batched publishes.
//
// Field ordering: map (8 B) > slice (24 B) > strings (16 B each) > AckMode (16 B) > uint16 (2 B).
type PublishRequest struct {
	Metadata       map[string]string `json:"metadata,omitempty"`
	Items          []PublishItem     `json:"items"`
	Namespace      string            `json:"namespace"`
	Model          string            `json:"model"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Ack            AckMode           `json:"ack,omitempty"`
	Dimensions     uint16            `json:"dimensions"`
}

// PublishResponse carries the server-assigned identifiers for the
// newly-published message.
type PublishResponse struct {
	MessageID string `json:"message_id"`
	Epoch     uint64 `json:"epoch"`
	Seq       uint64 `json:"seq"`
}

// Publish ingests a single message into the namespace. On success the
// response carries a stable MessageID and ordering tokens.
func (c *Client) Publish(ctx context.Context, req PublishRequest) (PublishResponse, error) {
	if req.Namespace == "" {
		req.Namespace = DefaultNamespace
	}
	applyNamespaceDefaults(req.Namespace, &req.Model, &req.Dimensions)
	if err := req.validate(); err != nil {
		return PublishResponse{}, err
	}
	var resp PublishResponse
	if err := c.doJSON(ctx, pathPublish, req, &resp, authBearer); err != nil {
		return PublishResponse{}, err
	}
	return resp, nil
}

func (r PublishRequest) validate() *Error {
	if r.Model == "" {
		return preflightErr("publish model must not be empty")
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return err
	}
	if len(r.Items) != 1 {
		return preflightErr("publish requires exactly 1 item, got %d", len(r.Items))
	}
	if err := validatePublishItem(r.Items[0]); err != nil {
		return err
	}
	// Vector ↔ dimensions agreement. The server checks this too, but
	// failing fast on a guaranteed-rejection request spares the round
	// trip and gives a clearer error message. Empty vector means
	// "text-only publish" and is allowed (text → server embedding).
	if v := r.Items[0].Vector; len(v) > 0 && len(v) != int(r.Dimensions) {
		return preflightErr(
			"publish vector length %d does not match dimensions %d",
			len(v), r.Dimensions,
		)
	}
	if err := validateMetadata(r.Metadata); err != nil {
		return err
	}
	if len(r.IdempotencyKey) > MaxIdempotencyKeyLen {
		return preflightErr("idempotency_key exceeds %d bytes", MaxIdempotencyKeyLen)
	}
	if !r.Ack.Valid() {
		return preflightErr("ack %q is not a valid AckMode", string(r.Ack))
	}
	return nil
}
