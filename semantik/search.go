package semantik

import "context"

// SearchRequest is the body of POST /v1/search.
//
// Namespace, Model and Dimensions are REQUIRED — the SDK applies no
// defaults. A request that leaves any of them unset is rejected at
// preflight rather than silently routed: defaulting Namespace to a
// shared value would let a forgotten field query a namespace the caller
// never intended, a data-isolation hazard. Model and Dimensions are
// model-coupled properties with no server default.
//
// A minimal request against the shared "global" namespace is therefore:
//
//	req := semantik.SearchRequest{
//	    Query:      `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 10`,
//	    Namespace:  "global",
//	    Model:      "Qwen3-Embedding-4B",
//	    Dimensions: 1024,
//	}
//
// Dimensions must match the output dimensionality of the embedding
// Model and, at query time, the dimensionality stored in the
// namespace. A mismatch is a server-side error.
//
// Limit zero means "use the SemQL LIMIT clause or the server default".
//
// Field ordering: strings (16 B each) > int (8 B) > uint16 (2 B).
type SearchRequest struct {
	Query      string `json:"query"`
	Namespace  string `json:"namespace"`
	Model      string `json:"model"`
	Limit      int    `json:"limit,omitempty"`
	Dimensions uint16 `json:"dimensions"`
}

// SearchResponse carries the ranked list of matches.
type SearchResponse struct {
	Results []ResultItem `json:"results,omitempty"`
}

// ResultItem is a single ranked match.
//
// Field ordering: map (8 B) > strings (16 B) > float32 (4 B).
type ResultItem struct {
	Metadata  map[string]string `json:"metadata,omitempty"`
	Content   string            `json:"content,omitempty"`
	MessageID string            `json:"message_id,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Score     float32           `json:"score,omitempty"`
}

// Search runs a SemQL query against the namespace. The returned
// SearchResponse is zero-valued when the request failed.
func (c *Client) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if err := req.validate(); err != nil {
		return SearchResponse{}, err
	}
	var resp SearchResponse
	if err := c.doJSON(ctx, pathSearch, req, &resp, authBearer); err != nil {
		return SearchResponse{}, err
	}
	return resp, nil
}

func (r SearchRequest) validate() *Error {
	if r.Query == "" {
		return preflightErr("search query must not be empty")
	}
	if err := validateTarget(r.Namespace, r.Model, r.Dimensions); err != nil {
		return err
	}
	if r.Limit < 0 {
		return preflightErr("search limit must not be negative")
	}
	return nil
}
