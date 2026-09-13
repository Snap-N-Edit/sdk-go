package snapnedit

import (
	"context"
	"net/http"
)

// DesignSpec is a declarative, multi-layer design — text, images, shapes,
// adjustments and effects over a frame. It is a large open JSON shape, so this
// SDK carries it untyped rather than pinning a snapshot of it: see the
// DesignSpec schema in the OpenAPI document at
// https://snapnedit.com/docs/api-reference for the fields.
type DesignSpec map[string]any

// DesignDocument is a compiled editor document — the output of
// [DesignsService.Create] and an accepted input to [DesignsService.Render].
// Treat it as opaque: hand it back verbatim, or load it in the editor.
type DesignDocument map[string]any

// RenderFormat is an output encoding for [DesignsService.Render]. A multi-page
// render is always a PDF.
type RenderFormat string

// The formats a design render can produce.
const (
	RenderPNG  RenderFormat = "png"
	RenderJPEG RenderFormat = "jpeg"
	RenderWebP RenderFormat = "webp"
	RenderAVIF RenderFormat = "avif"
	RenderPDF  RenderFormat = "pdf"
)

// DesignRenderRequest names what to render. Give exactly one of Spec, Document,
// Pages or Documents; Pages and Documents (or an explicit PDF format) produce a
// multi-page PDF.
type DesignRenderRequest struct {
	// Spec is a single-page design to compile and render.
	Spec DesignSpec `json:"spec,omitempty"`
	// Document is an already-compiled single page.
	Document DesignDocument `json:"document,omitempty"`
	// Pages is one spec per page, in order.
	Pages []DesignSpec `json:"pages,omitempty"`
	// Documents is one compiled document per page, in order.
	Documents []DesignDocument `json:"documents,omitempty"`
	// Format is the output encoding. Defaults to png server-side.
	Format RenderFormat `json:"format,omitempty"`
}

// RenderResult is the bytes of a rendered design.
type RenderResult struct {
	// Bytes is the rendered image or PDF.
	Bytes []byte
	// ContentType is the media type the server served them with.
	ContentType string
}

// DesignsService composes and renders designs server-side — no browser
// involved. Reach it through [Client.Designs].
type DesignsService struct {
	client *Client
}

// Create compiles a [DesignSpec] into a [DesignDocument] — POST /designs. This
// is how an agent composes a design rather than just running an image
// operation. Feed the document to [DesignsService.Render], or load it in the
// editor.
func (s *DesignsService) Create(ctx context.Context, spec DesignSpec) (DesignDocument, error) {
	body, err := jsonBody(spec)
	if err != nil {
		return nil, err
	}
	var out struct {
		Document DesignDocument `json:"document"`
	}
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/designs",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return out.Document, nil
}

// CreatePages compiles a multi-page design — POST /designs with `{ pages }`.
// It returns one compiled document per page, in order.
func (s *DesignsService) CreatePages(ctx context.Context, pages []DesignSpec) ([]DesignDocument, error) {
	body, err := jsonBody(map[string]any{"pages": pages})
	if err != nil {
		return nil, err
	}
	var out struct {
		Documents []DesignDocument `json:"documents"`
	}
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/designs",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return out.Documents, nil
}

// Render rasterizes a design to image bytes or a PDF — POST /designs/render.
// The response is raw bytes, not JSON.
func (s *DesignsService) Render(ctx context.Context, req DesignRenderRequest) (*RenderResult, error) {
	body, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.send(ctx, request{
		method:      http.MethodPost,
		path:        "/designs/render",
		body:        body,
		contentType: "application/json",
		auth:        true,
	})
	if err != nil {
		return nil, err
	}
	return &RenderResult{Bytes: resp.body, ContentType: resp.contentType()}, nil
}
