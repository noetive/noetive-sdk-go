package semantik

import (
	"strings"
	"testing"
)

func TestHasControlChar(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hello", false},
		{"", false},
		{"line\n", true},
		{"tab\t", true},
		{"\x00", true},
		{"\x7f", true},
		{"unicode: café", false},
	}
	for _, tc := range cases {
		if got := hasControlChar(tc.in); got != tc.want {
			t.Errorf("hasControlChar(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidateMetadata_Boundaries(t *testing.T) {
	if err := validateMetadata(nil); err != nil {
		t.Errorf("nil metadata should pass, got %v", err)
	}

	// Boundary: exactly MaxMetadataKeys keys is OK.
	ok := make(map[string]string, MaxMetadataKeys)
	for i := range MaxMetadataKeys {
		ok[string(rune('a'+i))] = "v"
	}
	if err := validateMetadata(ok); err != nil {
		t.Errorf("max keys should pass, got %v", err)
	}

	// Boundary+1: one too many.
	ok[string(rune('a'+MaxMetadataKeys))] = "v"
	if err := validateMetadata(ok); err == nil {
		t.Error("exceeding key count should fail")
	}

	// Value length boundary.
	big := map[string]string{"k": strings.Repeat("x", MaxMetadataValueLen+1)}
	if err := validateMetadata(big); err == nil {
		t.Error("oversized value should fail")
	}

	// Key length boundary.
	big2 := map[string]string{strings.Repeat("x", MaxMetadataKeyLen+1): "v"}
	if err := validateMetadata(big2); err == nil {
		t.Error("oversized key should fail")
	}

	// Total bytes boundary.
	total := map[string]string{}
	v := strings.Repeat("x", MaxMetadataValueLen)
	// Need enough entries that key+value sum exceeds MaxMetadataTotalBytes.
	// Each entry contributes ~1 + MaxMetadataValueLen bytes.
	needed := MaxMetadataTotalBytes/(1+MaxMetadataValueLen) + 1
	for i := 0; i < needed && i < MaxMetadataKeys; i++ {
		total[string(rune('a'+i))] = v
	}
	// This may fail either on key count or total bytes; both are "exceed".
	if err := validateMetadata(total); err == nil {
		t.Error("oversized total bytes should fail")
	}
}

func TestValidatePublishItem_Boundaries(t *testing.T) {
	cases := []struct {
		name     string
		item     PublishItem
		wantFail bool
	}{
		{"empty", PublishItem{}, true},
		{"both text and vector", PublishItem{Text: "x", Vector: []float32{1}}, false},
		{"text ok", PublishItem{Text: "hello"}, false},
		{"text too large", PublishItem{Text: strings.Repeat("a", MaxTextBytes+1)}, true},
		{"vector ok", PublishItem{Vector: []float32{1, 2, 3}}, false},
		{"vector too large", PublishItem{Vector: make([]float32, MaxVectorDim+1)}, true},
		{"vector with NaN", PublishItem{Vector: []float32{1, float32NaN(), 3}}, true},
		{"vector with Inf", PublishItem{Vector: []float32{1, float32Inf(), 3}}, true},
	}
	for _, tc := range cases {
		err := validatePublishItem(tc.item)
		if (err != nil) != tc.wantFail {
			t.Errorf("%s: err = %v, wantFail = %v", tc.name, err, tc.wantFail)
		}
	}
}

func float32NaN() float32 {
	var n float32
	n = n / n
	return n
}

func float32Inf() float32 {
	var n float32 = 1
	var z float32
	return n / z
}
