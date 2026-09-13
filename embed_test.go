package snapnedit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestEmbedCreateSessionSendsNoAuthorization(t *testing.T) {
	var body map[string]any
	var auth string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 200, map[string]string{"token": "et_1", "expiresAt": "2030-01-01T00:00:00Z"})
	}))
	token, err := c.Embed.CreateSession(context.Background(), "pk_live_x", "http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "et_1" {
		t.Fatalf("token = %+v", token)
	}
	if auth != "" {
		t.Fatal("the publishable key IS the credential — no bearer header belongs on this call")
	}
	if body["publishableKey"] != "pk_live_x" || body["hostOrigin"] != "http://localhost:3000" {
		t.Fatalf("body = %+v", body)
	}
}

func TestEmbedCreateSessionDisallowedOrigin(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 403, "forbidden", "origin not allowed")
	}))
	_, err := c.Embed.CreateSession(context.Background(), "pk_live_x", "https://not-allowed.example")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

func TestEmbedCreateTokenOmitsUnsetScopes(t *testing.T) {
	var body map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("minting a token requires the secret key")
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 200, map[string]string{"token": "et_2", "expiresAt": "2030"})
	}))
	if _, err := c.Embed.CreateToken(context.Background(), EmbedTokenRequest{TTLSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	if body["ttlSeconds"].(float64) != 600 {
		t.Fatalf("body = %+v", body)
	}
	for _, key := range []string{"endUserId", "maxCredits", "allowedOperations", "origin"} {
		if _, present := body[key]; present {
			t.Fatalf("%s should be omitted when unset", key)
		}
	}
}

func TestEmbedCreateTokenNeedsAKey(t *testing.T) {
	_, err := NewClient("").Embed.CreateToken(context.Background(), EmbedTokenRequest{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestDesignsRenderReturnsRawBytes(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/designs/render" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7"))
	}))
	out, err := c.Designs.Render(context.Background(), DesignRenderRequest{
		Pages:  []DesignSpec{{"width": 100}, {"width": 100}},
		Format: RenderPDF,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Bytes) != "%PDF-1.7" || out.ContentType != "application/pdf" {
		t.Fatalf("out = %+v", out)
	}
}

func TestDesignsCreate(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"document": map[string]any{"id": "doc-1"}})
	}))
	doc, err := c.Designs.Create(context.Background(), DesignSpec{"width": 100, "height": 100})
	if err != nil {
		t.Fatal(err)
	}
	if doc["id"] != "doc-1" {
		t.Fatalf("doc = %+v", doc)
	}
}
