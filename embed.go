package snapnedit

import (
	"context"
	"net/http"
)

// EmbedToken is a short-lived bearer credential for the embedded editor. It
// authenticates as the account and is billed exactly like an API key, so treat
// it as a secret and keep its TTL short.
type EmbedToken struct {
	// Token is the signed bearer token.
	Token string `json:"token"`
	// ExpiresAt is an RFC 3339 timestamp.
	ExpiresAt string `json:"expiresAt"`
}

// EmbedTokenRequest are the optional scopes on a host-minted embed token. Every
// field is optional; the zero value asks for a default-TTL, unscoped token.
type EmbedTokenRequest struct {
	// TTLSeconds is how long the token lives, 60..86400.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
	// EndUserID attributes the session to one of your own users.
	EndUserID string `json:"endUserId,omitempty"`
	// MaxCredits caps what this token may spend.
	MaxCredits int `json:"maxCredits,omitempty"`
	// AllowedOperations restricts the token to these operations.
	AllowedOperations []Operation `json:"allowedOperations,omitempty"`
	// Origin stamps the browser origin the token is intended for.
	Origin string `json:"origin,omitempty"`
}

// EmbedService mints the tokens the embedded editor authenticates with. Reach
// it through [Client.Embed].
type EmbedService struct {
	client *Client
}

// CreateSession exchanges a publishable pk_ key for a short-lived embed token
// — POST /embed/sessions. It sends no Authorization header: the publishable
// key IS the credential, and presenting one as a bearer resolves to anonymous
// instead ([CodeUnauthorized]).
//
// hostOrigin identifies the surface embedding the editor and is matched against
// the key's allowed origins: a browser origin ("https://app.example.com"), or a
// native shell id ("native:com.acme.photos") for a WebView host that has no
// origin. An origin that is not on the allowlist is [CodeForbidden].
func (s *EmbedService) CreateSession(ctx context.Context, publishableKey, hostOrigin string) (*EmbedToken, error) {
	body, err := jsonBody(map[string]string{"publishableKey": publishableKey, "hostOrigin": hostOrigin})
	if err != nil {
		return nil, err
	}
	var out EmbedToken
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/embed/sessions",
		body:        body,
		contentType: "application/json",
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateToken mints a scoped embed token from your server — POST /embed/tokens.
// It requires the account's secret sk_ key, so call it server-side and hand the
// short-lived token to the browser.
func (s *EmbedService) CreateToken(ctx context.Context, req EmbedTokenRequest) (*EmbedToken, error) {
	if err := s.client.requireKey("minting an embed token"); err != nil {
		return nil, err
	}
	body, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var out EmbedToken
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/embed/tokens",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
