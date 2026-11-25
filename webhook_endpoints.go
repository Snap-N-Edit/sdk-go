package snapnedit

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Snap-N-Edit/sdk-go/webhooks"
)

// WebhookEndpoint is a registered webhook URL as every read returns it — never
// the signing secret.
type WebhookEndpoint struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	URL       string `json:"url"`
	// Events are the event types delivered to this endpoint.
	Events []webhooks.EventType `json:"events"`
	// Active is false for a paused endpoint, which receives nothing.
	Active bool `json:"active"`
	// CreatedAt and UpdatedAt are RFC 3339 timestamps.
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// WebhookEndpointInput is the body of [WebhookEndpointsService.Create].
type WebhookEndpointInput struct {
	// URL must be https and publicly routable.
	URL string `json:"url"`
	// Events to subscribe to. Nil means all of them.
	Events []webhooks.EventType `json:"events,omitempty"`
}

// WebhookEndpointCreated is what [WebhookEndpointsService.Create] returns. The
// secret is shown here and on rotate only — store it.
type WebhookEndpointCreated struct {
	Endpoint WebhookEndpoint `json:"endpoint"`
	Secret   string          `json:"secret"`
}

// WebhookEndpointsService manages the account's webhook endpoints at
// /webhook-endpoints. Secret keys only.
//
// Reach it through [Client.WebhookEndpoints].
type WebhookEndpointsService struct {
	client *Client
}

func webhookEndpointPath(id, suffix string) string {
	return "/webhook-endpoints/" + url.PathEscape(id) + suffix
}

// List returns every webhook endpoint on the account — GET /webhook-endpoints.
func (s *WebhookEndpointsService) List(ctx context.Context) ([]WebhookEndpoint, error) {
	if err := s.client.requireKey("listing webhook endpoints"); err != nil {
		return nil, err
	}
	var out struct {
		Endpoints []WebhookEndpoint `json:"endpoints"`
	}
	if err := s.client.doJSON(ctx, request{method: http.MethodGet, path: "/webhook-endpoints", auth: true}, &out); err != nil {
		return nil, err
	}
	return out.Endpoints, nil
}

// Create registers an endpoint — POST /webhook-endpoints. A non-https or
// private URL, or an unknown event type, is [CodeInvalidInput].
func (s *WebhookEndpointsService) Create(ctx context.Context, input WebhookEndpointInput) (*WebhookEndpointCreated, error) {
	if err := s.client.requireKey("creating a webhook endpoint"); err != nil {
		return nil, err
	}
	body, err := jsonBody(input)
	if err != nil {
		return nil, err
	}
	var out WebhookEndpointCreated
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/webhook-endpoints",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetActive pauses or resumes an endpoint — PATCH /webhook-endpoints/{id}.
func (s *WebhookEndpointsService) SetActive(ctx context.Context, id string, active bool) (*WebhookEndpoint, error) {
	if err := s.client.requireKey("updating a webhook endpoint"); err != nil {
		return nil, err
	}
	body, err := jsonBody(map[string]bool{"active": active})
	if err != nil {
		return nil, err
	}
	var out struct {
		Endpoint WebhookEndpoint `json:"endpoint"`
	}
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPatch,
		path:        webhookEndpointPath(id, ""),
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out.Endpoint, nil
}

// Delete removes an endpoint — DELETE /webhook-endpoints/{id}. An id that is
// not on the account is [CodeNotFound].
func (s *WebhookEndpointsService) Delete(ctx context.Context, id string) error {
	if err := s.client.requireKey("deleting a webhook endpoint"); err != nil {
		return err
	}
	return s.client.doJSON(ctx, request{method: http.MethodDelete, path: webhookEndpointPath(id, ""), auth: true}, nil)
}

// RotateSecret replaces an endpoint's signing secret and returns the new one —
// POST /webhook-endpoints/{id}/rotate.
func (s *WebhookEndpointsService) RotateSecret(ctx context.Context, id string) (string, error) {
	if err := s.client.requireKey("rotating a webhook secret"); err != nil {
		return "", err
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := s.client.doJSON(ctx, request{method: http.MethodPost, path: webhookEndpointPath(id, "/rotate"), auth: true}, &out); err != nil {
		return "", err
	}
	return out.Secret, nil
}
