package semantik

import "context"

// SearchRequest is the body of POST /v1/search.
//
// All fields are optional when targeting the default configuration:
//
//   - Namespace empty ⇒ [DefaultNamespace] ("global")
//   - Model empty + namespace is global ⇒ [DefaultModel]
//   - Dimensions zero + namespace is global ⇒ [DefaultDimensions]
//
// A minimal request is therefore just SearchRequest{Query: "..."}.
// Callers targeting a private namespace MUST set Model and Dimensions
// explicitly; the SDK does not guess their configuration.
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
	if req.Namespace == "" {
		req.Namespace = DefaultNamespace
	}
	applyNamespaceDefaults(req.Namespace, &req.Model, &req.Dimensions)
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
	if r.Model == "" {
		return preflightErr("search model must not be empty")
	}
	if err := validateDimensions(r.Dimensions); err != nil {
		return err
	}
	if r.Limit < 0 {
		return preflightErr("search limit must not be negative")
	}
	return nil
}
