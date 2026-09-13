package snapnedit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// StorageProvider is the flavour of S3-compatible storage a saved destination
// points at.
type StorageProvider string

// The providers a saved destination may use.
const (
	ProviderAWSS3        StorageProvider = "aws-s3"
	ProviderCloudflareR2 StorageProvider = "cloudflare-r2"
	ProviderBackblazeB2  StorageProvider = "backblaze-b2"
	ProviderS3Compatible StorageProvider = "s3-compatible"
)

// StorageDestinationTest records the last credential probe run against a saved
// destination.
type StorageDestinationTest struct {
	// Status is "ok" or "failed".
	Status string `json:"status"`
	// At is an RFC 3339 timestamp.
	At string `json:"at"`
	// Error explains a failed probe.
	Error string `json:"error,omitempty"`
}

// StorageDestination is one saved bucket on the account. The secret access key
// is encrypted at rest and is never returned by any endpoint — only the last
// four characters of the access key id are echoed.
type StorageDestination struct {
	// ID names this destination on a job (`destination: { type: "saved", id }`).
	ID string `json:"id"`
	// Name is the account's own label for it.
	Name string `json:"name"`
	// Provider is the storage flavour.
	Provider StorageProvider `json:"provider"`
	// Bucket is the bucket results are written to.
	Bucket string `json:"bucket"`
	// Region is the bucket region, or nil.
	Region *string `json:"region"`
	// Endpoint is the S3 endpoint for a non-AWS provider, or nil.
	Endpoint *string `json:"endpoint"`
	// ForcePathStyle addresses the bucket as a path rather than a subdomain.
	ForcePathStyle bool `json:"forcePathStyle"`
	// KeyPrefix is prepended to every object key written here.
	KeyPrefix string `json:"keyPrefix"`
	// AccessKeyIDLast4 is the last 4 characters of the access key id.
	AccessKeyIDLast4 string `json:"accessKeyIdLast4"`
	// IsDefault reports whether jobs that name no destination are delivered
	// here. At most one destination per account is the default.
	IsDefault bool `json:"isDefault"`
	// DeleteAfterDelivery drops snapnedit's own copy of a result once the
	// bucket confirms the write. A job delivered here reports Download nil.
	DeleteAfterDelivery bool `json:"deleteAfterDelivery"`
	// LastTest is the last credential probe, or nil if never tested.
	LastTest *StorageDestinationTest `json:"lastTest"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is an RFC 3339 timestamp.
	UpdatedAt string `json:"updatedAt"`
}

// StorageDestinationInput is the body of [DestinationsService.Create]. Name,
// Provider, Bucket, AccessKeyID and SecretAccessKey are required; which of the
// rest are needed depends on the provider (an endpoint for s3-compatible, a
// region for aws-s3, an account id for cloudflare-r2).
type StorageDestinationInput struct {
	// Name is your label for the destination, 1..80 characters.
	Name string `json:"name"`
	// Provider is the storage flavour.
	Provider StorageProvider `json:"provider"`
	// Bucket is the bucket to write to, 3..63 characters.
	Bucket string `json:"bucket"`
	// Region is the bucket region, lower-case and hyphenated.
	Region string `json:"region,omitempty"`
	// Endpoint is the S3 endpoint for a non-AWS provider.
	Endpoint string `json:"endpoint,omitempty"`
	// AccountID is the Cloudflare account id, for cloudflare-r2.
	AccountID string `json:"accountId,omitempty"`
	// ForcePathStyle addresses the bucket as a path rather than a subdomain.
	ForcePathStyle *bool `json:"forcePathStyle,omitempty"`
	// KeyPrefix is prepended to every object key written here.
	KeyPrefix string `json:"keyPrefix,omitempty"`
	// AccessKeyID is the S3 access key id.
	AccessKeyID string `json:"accessKeyId"`
	// SecretAccessKey is the S3 secret. Encrypted at rest, never echoed back.
	SecretAccessKey string `json:"secretAccessKey"`
	// IsDefault makes this the account default, replacing any existing one.
	IsDefault *bool `json:"isDefault,omitempty"`
	// DeleteAfterDelivery drops snapnedit's copy once the bucket confirms.
	DeleteAfterDelivery *bool `json:"deleteAfterDelivery,omitempty"`
}

// StorageDestinationPatch is the body of [DestinationsService.Update]. Every
// field is optional and a nil field is left alone. AccessKeyID and
// SecretAccessKey must move together, and Provider is not patchable — change
// it with a delete plus a create.
type StorageDestinationPatch struct {
	// Name replaces the label.
	Name *string
	// Bucket replaces the bucket.
	Bucket *string
	// Region replaces the region. Set ClearRegion to unset it instead.
	Region *string
	// ClearRegion sends `region: null`.
	ClearRegion bool
	// Endpoint replaces the endpoint. Set ClearEndpoint to unset it instead.
	Endpoint *string
	// ClearEndpoint sends `endpoint: null`.
	ClearEndpoint bool
	// ForcePathStyle replaces the addressing style.
	ForcePathStyle *bool
	// KeyPrefix replaces the key prefix.
	KeyPrefix *string
	// AccessKeyID replaces the access key id. Send SecretAccessKey with it.
	AccessKeyID *string
	// SecretAccessKey replaces the secret. Send AccessKeyID with it.
	SecretAccessKey *string
	// IsDefault makes this the account default (or clears the flag).
	IsDefault *bool
	// DeleteAfterDelivery toggles dropping snapnedit's own copy.
	DeleteAfterDelivery *bool
}

// MarshalJSON emits only the fields that were set, so a PATCH never
// accidentally resets something. Region and Endpoint are the two fields that
// can be explicitly nulled, which is why they each have a Clear flag.
func (p StorageDestinationPatch) MarshalJSON() ([]byte, error) {
	body := map[string]any{}
	if p.Name != nil {
		body["name"] = *p.Name
	}
	if p.Bucket != nil {
		body["bucket"] = *p.Bucket
	}
	switch {
	case p.ClearRegion:
		body["region"] = nil
	case p.Region != nil:
		body["region"] = *p.Region
	}
	switch {
	case p.ClearEndpoint:
		body["endpoint"] = nil
	case p.Endpoint != nil:
		body["endpoint"] = *p.Endpoint
	}
	if p.ForcePathStyle != nil {
		body["forcePathStyle"] = *p.ForcePathStyle
	}
	if p.KeyPrefix != nil {
		body["keyPrefix"] = *p.KeyPrefix
	}
	if p.AccessKeyID != nil {
		body["accessKeyId"] = *p.AccessKeyID
	}
	if p.SecretAccessKey != nil {
		body["secretAccessKey"] = *p.SecretAccessKey
	}
	if p.IsDefault != nil {
		body["isDefault"] = *p.IsDefault
	}
	if p.DeleteAfterDelivery != nil {
		body["deleteAfterDelivery"] = *p.DeleteAfterDelivery
	}
	return json.Marshal(body)
}

// DestinationTestResult is the outcome of [DestinationsService.Test] — a real
// round trip against the bucket (a probe object written under the
// destination's own prefix, then deleted), not a credential format check. The
// call always succeeds; Ok says whether the bucket did.
type DestinationTestResult struct {
	// Ok reports whether the probe wrote and deleted successfully.
	Ok bool `json:"ok"`
	// LatencyMs is how long the probe took.
	LatencyMs float64 `json:"latencyMs"`
	// Error explains a failed probe.
	Error string `json:"error,omitempty"`
}

// PresignExt is a file extension [DestinationsService.PresignUpload] can sign
// an object for.
type PresignExt string

// The extensions a presigned upload accepts.
const (
	ExtPNG  PresignExt = "png"
	ExtJPG  PresignExt = "jpg"
	ExtWebP PresignExt = "webp"
	ExtAVIF PresignExt = "avif"
	ExtSVG  PresignExt = "svg"
	ExtPDF  PresignExt = "pdf"
	ExtGIF  PresignExt = "gif"
)

// PresignRequest asks for a one-shot signed PUT into your own bucket.
// ContentType must be the canonical type for Ext; a mismatched pair is
// [CodeInvalidInput].
type PresignRequest struct {
	// Ext is the object's file extension.
	Ext PresignExt `json:"ext"`
	// ContentType is the canonical media type for Ext.
	ContentType string `json:"contentType"`
}

// PresignResponse is a short-lived (15 minute) signed PUT for one object in
// your bucket — for uploading something you produced yourself, such as an
// editor export, without putting S3 credentials in a browser.
type PresignResponse struct {
	// URL is the signed PUT.
	URL string `json:"url"`
	// Method is always "PUT".
	Method string `json:"method"`
	// Headers must be sent verbatim on the PUT: the signature covers them.
	Headers map[string]string `json:"headers"`
	// Key is the object key, <keyPrefix>YYYY/MM/DD/export-<ts>-<6 chars>.<ext>.
	Key string `json:"key"`
	// Bucket is the destination's bucket.
	Bucket string `json:"bucket"`
	// ExpiresAt is an RFC 3339 timestamp.
	ExpiresAt string `json:"expiresAt"`
}

// DestinationsService manages the account's saved storage destinations — its
// own S3-compatible buckets, saved once and then named by id on a job
// (`destination: { type: "saved", id }`), or marked as the account default so
// every job is delivered to them with no destination field at all.
//
// Reach it through [Client.Destinations].
type DestinationsService struct {
	client *Client
}

// destinationEnvelope wraps a single destination in API responses.
type destinationEnvelope struct {
	Destination StorageDestination `json:"destination"`
}

// destinationListEnvelope wraps the list response.
type destinationListEnvelope struct {
	Destinations []StorageDestination `json:"destinations"`
}

// path builds a /destinations URL, escaping the id.
func destinationPath(id, suffix string) string {
	return "/destinations/" + url.PathEscape(id) + suffix
}

// List returns every saved destination on the account — GET /destinations.
// Credentials are never included; only the access key's last 4 characters are.
func (s *DestinationsService) List(ctx context.Context) ([]StorageDestination, error) {
	if err := s.client.requireKey("listing destinations"); err != nil {
		return nil, err
	}
	var out destinationListEnvelope
	if err := s.client.doJSON(ctx, request{method: http.MethodGet, path: "/destinations", auth: true}, &out); err != nil {
		return nil, err
	}
	return out.Destinations, nil
}

// Create saves a bucket and its credentials — POST /destinations. The secret
// access key is encrypted at rest and never returned, including by this call.
// Ten destinations per account is the ceiling.
func (s *DestinationsService) Create(ctx context.Context, input StorageDestinationInput) (*StorageDestination, error) {
	if err := s.client.requireKey("creating a destination"); err != nil {
		return nil, err
	}
	body, err := jsonBody(input)
	if err != nil {
		return nil, err
	}
	var out destinationEnvelope
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/destinations",
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out.Destination, nil
}

// Update patches a saved destination — PATCH /destinations/{id}. A destination
// id that is not on the caller's account is [CodeNotFound], never
// [CodeForbidden].
func (s *DestinationsService) Update(ctx context.Context, id string, patch StorageDestinationPatch) (*StorageDestination, error) {
	if err := s.client.requireKey("updating a destination"); err != nil {
		return nil, err
	}
	body, err := jsonBody(patch)
	if err != nil {
		return nil, err
	}
	var out destinationEnvelope
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPatch,
		path:        destinationPath(id, ""),
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out.Destination, nil
}

// Delete removes a saved destination — DELETE /destinations/{id}. Deleting the
// default simply leaves the account without one; jobs already queued against
// it stop being delivered.
func (s *DestinationsService) Delete(ctx context.Context, id string) error {
	if err := s.client.requireKey("deleting a destination"); err != nil {
		return err
	}
	return s.client.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   destinationPath(id, ""),
		auth:   true,
	}, nil)
}

// Test proves the credentials can WRITE, by writing and then deleting a probe
// object under the destination's own prefix — POST /destinations/{id}/test.
//
// It returns an error only when the HTTP call itself failed. A bucket that
// refused the probe comes back as a result with Ok false.
func (s *DestinationsService) Test(ctx context.Context, id string) (*DestinationTestResult, error) {
	if err := s.client.requireKey("testing a destination"); err != nil {
		return nil, err
	}
	var out DestinationTestResult
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        destinationPath(id, "/test"),
		body:        []byte("{}"),
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PresignUpload mints a short-lived signed PUT for one object in your own
// bucket — POST /destinations/{id}/presign. Send the returned headers verbatim
// on the PUT; the signature covers them.
func (s *DestinationsService) PresignUpload(ctx context.Context, id string, req PresignRequest) (*PresignResponse, error) {
	if err := s.client.requireKey("presigning an upload"); err != nil {
		return nil, err
	}
	body, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var out PresignResponse
	if err := s.client.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        destinationPath(id, "/presign"),
		body:        body,
		contentType: "application/json",
		auth:        true,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
