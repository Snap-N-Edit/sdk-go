package snapnedit

import (
	"context"
	"net/http"
)

// SignedURL is a short-lived, self-authenticating URL the API mints — for
// uploading an asset or downloading a result. The URL may be a same-origin
// path rather than an absolute URL; this client resolves it against the base
// URL for you.
type SignedURL struct {
	// URL is the location to PUT to or GET from.
	URL string `json:"url"`
	// ExpiresAt is an RFC 3339 timestamp after which the URL stops working.
	ExpiresAt string `json:"expiresAt"`
}

// UploadResult identifies an uploaded and confirmed asset.
type UploadResult struct {
	// AssetID names the asset in a job's input or in params.maskAssetId.
	AssetID string `json:"assetId"`
	// ContentHash is the SHA-256 of the stored bytes, 64 lower-case hex
	// characters. It is what result caching keys on.
	ContentHash string `json:"contentHash"`
	// Bytes is the confirmed byte length of the stored object.
	Bytes int `json:"bytes"`
}

// createUploadResponse is the body of POST /uploads.
type createUploadResponse struct {
	AssetID string    `json:"assetId"`
	Upload  SignedURL `json:"upload"`
}

// Upload stores one image and returns its asset id, performing all three steps
// of the upload flow: POST /uploads for a presigned PUT, the PUT itself, then
// POST /uploads/{id}/confirm.
//
// The confirm step is not optional. Until it runs the asset carries a
// placeholder hash and cannot participate in content-based result caching.
//
// A [FromURL] or [FromAsset] input has nothing to upload and is rejected with
// [CodeInvalidInput].
func (c *Client) Upload(ctx context.Context, input Input) (*UploadResult, error) {
	if err := c.requireKey("uploading"); err != nil {
		return nil, err
	}
	data, err := input.bytes()
	if err != nil {
		return nil, err
	}
	mime := input.mime
	if mime == "" {
		mime = sniffMime(data)
	}

	body, err := jsonBody(map[string]any{"mime": mime, "bytes": len(data)})
	if err != nil {
		return nil, err
	}
	var created createUploadResponse
	if err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/uploads",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &created); err != nil {
		return nil, err
	}

	// The presigned PUT authenticates itself through its query string, so it
	// deliberately carries no Authorization header — some presigned schemes
	// (S3 SigV4 among them) reject a second, conflicting credential.
	if _, err := c.send(ctx, request{
		method:      http.MethodPut,
		rawURL:      created.Upload.URL,
		body:        data,
		contentType: mime,
	}); err != nil {
		return nil, err
	}

	var confirmed UploadResult
	if err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/uploads/" + created.AssetID + "/confirm",
		auth:   true,
	}, &confirmed); err != nil {
		return nil, err
	}
	if confirmed.AssetID == "" {
		confirmed.AssetID = created.AssetID
	}
	return &confirmed, nil
}

// Download fetches the bytes behind a [SignedURL] — a result's download URL,
// most often. It returns the bytes and the media type the server served them
// with.
func (c *Client) Download(ctx context.Context, signed SignedURL) ([]byte, string, error) {
	resp, err := c.send(ctx, request{method: http.MethodGet, rawURL: signed.URL})
	if err != nil {
		return nil, "", err
	}
	mime := resp.contentType()
	if mime == "" {
		mime = "application/octet-stream"
	}
	return resp.body, mime, nil
}
