package semantik

import (
	"errors"
	"net/http"
	"testing"
)

func TestSearch_HappyPath(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathSearch {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("Authorization = %q", got)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var req SearchRequest
		readJSON(t, r, &req)
		if req.Query == "" || req.Namespace == "" || req.Model == "" {
			t.Errorf("missing required fields: %+v", req)
		}
		writeJSON(t, w, http.StatusOK, SearchResponse{
			Results: []ResultItem{{MessageID: "msg_1", Score: 0.9, Content: "hi"}},
		})
	})
	res, err := c.Search(t.Context(), SearchRequest{
		Query: `MATCH DISTANCE("ml") WITHIN 0.4`, Namespace: "n", Model: "m", Dimensions: 384, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].MessageID != "msg_1" {
		t.Errorf("unexpected results: %+v", res)
	}
}

func TestSearch_PreflightRejectsEmptyQuery(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Search(t.Context(), SearchRequest{Namespace: "n", Model: "m", Dimensions: 384})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 0 {
		t.Errorf("preflight error should have HTTPStatus == 0, got %+v", apiErr)
	}
}

func TestSearch_PreflightRejectsBadDimensions(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 0})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	_, err = c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: MaxVectorDim + 1})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
}

func TestSearch_RateLimited(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusTooManyRequests, CodeRateLimited, "", 0)
	})
	_, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 384})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("want ErrRateLimited, got %v", err)
	}
}

func TestSearch_DefaultsWhenAllUnset(t *testing.T) {
	var body SearchRequest
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &body)
		writeJSON(t, w, http.StatusOK, SearchResponse{})
	})
	_, err := c.Search(t.Context(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
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

func TestSearch_DefaultsDoNotOverrideExplicit(t *testing.T) {
	var body SearchRequest
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &body)
		writeJSON(t, w, http.StatusOK, SearchResponse{})
	})
	_, err := c.Search(t.Context(), SearchRequest{
		Query: "q", Namespace: DefaultNamespace,
		Model: "custom-model", Dimensions: 384,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if body.Model != "custom-model" {
		t.Errorf("SDK overrode explicit Model: got %q", body.Model)
	}
	if body.Dimensions != 384 {
		t.Errorf("SDK overrode explicit Dimensions: got %d", body.Dimensions)
	}
}

func TestSearch_NoModelDefaultForPrivateNamespace(t *testing.T) {
	// When the caller targets a non-default namespace, the SDK must
	// NOT fill in Model/Dimensions — private namespaces have their
	// own configuration.
	c, _ := New(testKey)
	_, err := c.Search(t.Context(), SearchRequest{
		Query: "q", Namespace: "private-x",
	})
	if err == nil {
		t.Fatal("expected preflight error (empty Model)")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}

func TestSearch_LimitOmittedWhenZero(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		readJSON(t, r, &body)
		if _, present := body["limit"]; present {
			t.Errorf("limit should be omitted when zero; body: %v", body)
		}
		writeJSON(t, w, http.StatusOK, SearchResponse{})
	})
	_, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 384})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
}
