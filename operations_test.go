package snapnedit

import (
	"context"
	"net/http"
	"testing"
)

func TestOperationsCatalogIsComplete(t *testing.T) {
	if len(Operations) != 17 {
		t.Fatalf("%d operations, want 17", len(Operations))
	}
	seen := map[Operation]bool{}
	for _, op := range Operations {
		if seen[op] {
			t.Fatalf("duplicate operation %s", op)
		}
		seen[op] = true
		if !op.Valid() {
			t.Fatalf("%s is in Operations but reports Valid() false", op)
		}
	}
	if Operation("not-an-op").Valid() {
		t.Fatal("an unknown id must not be Valid")
	}
}

func TestCreditCostsAndMaskFlags(t *testing.T) {
	if got := OpResizeImage.CreditCost(); got != 0 {
		t.Fatalf("resize-image costs %d, want 0 (it is the free operation)", got)
	}
	if got := OpRemoveBackground.CreditCost(); got != 1 {
		t.Fatalf("remove-background costs %d, want 1", got)
	}
	if got := OpUpscale.CreditCost(); got != 2 {
		t.Fatalf("upscale costs %d, want 2", got)
	}
	if got := OpGenerativeFill.CreditCost(); got != 3 {
		t.Fatalf("generative-fill costs %d, want 3", got)
	}

	var masked []Operation
	for _, op := range Operations {
		if op.RequiresMask() {
			masked = append(masked, op)
		}
	}
	want := map[Operation]bool{OpMagicEraser: true, OpGenerativeFill: true, OpRemoveWatermark: true}
	if len(masked) != len(want) {
		t.Fatalf("mask-guided operations = %v", masked)
	}
	for _, op := range masked {
		if !want[op] {
			t.Fatalf("%s should not require a mask", op)
		}
	}
}

func TestListOperations(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(w, 200, []map[string]any{{
			"id": "upscale", "label": "Upscale", "accept": []string{"image/png"},
			"maxInputDimension": 4096, "paramsJsonSchema": map[string]any{"type": "object"},
			"requiresMask": false, "description": "d", "seoTitle": "s",
		}})
	}))
	ops, err := c.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].ID != OpUpscale || ops[0].MaxInputDimension != 4096 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestErrorCodesAreTheClosedSet(t *testing.T) {
	if len(ErrorCodes) != 13 {
		t.Fatalf("%d error codes, want 13", len(ErrorCodes))
	}
	for _, code := range ErrorCodes {
		if !IsKnownErrorCode(code) {
			t.Fatalf("%s is in ErrorCodes but not known", code)
		}
	}
	if IsKnownErrorCode("teapot") {
		t.Fatal("an unknown code must not be known")
	}
	if got := asErrorCode("teapot"); got != CodeInternal {
		t.Fatalf("asErrorCode(unknown) = %s, want internal", got)
	}
}
