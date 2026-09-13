package snapnedit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// JobState is where a job is in its lifecycle.
type JobState string

// The job states. Succeeded, Failed and Canceled are terminal.
const (
	JobQueued     JobState = "queued"
	JobProcessing JobState = "processing"
	JobSucceeded  JobState = "succeeded"
	JobFailed     JobState = "failed"
	JobCanceled   JobState = "canceled"
)

// JobInputKind says where a job read its image from.
type JobInputKind string

// The input kinds. The API never echoes the URL itself — a presigned GET is a
// bearer credential for someone's bucket — so a caller only learns the kind.
const (
	InputKindAsset JobInputKind = "asset"
	InputKindURL   JobInputKind = "url"
)

// JobInput is the input half of a job's bring-your-own-storage envelope.
type JobInput struct {
	// Kind is "asset" or "url".
	Kind JobInputKind `json:"kind"`
}

// DestinationType distinguishes a URL you signed from one of your saved
// storage destinations.
type DestinationType string

// The destination types.
const (
	// DestinationPresignedPut is a PUT URL you signed yourself.
	DestinationPresignedPut DestinationType = "presigned-put"
	// DestinationSaved names one of the account's saved storage destinations.
	DestinationSaved DestinationType = "saved"
)

// DeliveryStatus is how a job's delivery into your own bucket went.
type DeliveryStatus string

// The delivery statuses. A failed delivery never fails the job: the result is
// still downloadable from snapnedit.
const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryFailed    DeliveryStatus = "failed"
)

// JobDestinationSummary is what the API reports about a job's destination. It
// never carries the URL or the headers of a presigned PUT — those are secrets
// in motion. A saved destination's id and name are the account's own, so they
// are safe to echo.
type JobDestinationSummary struct {
	// Type is "presigned-put" or "saved".
	Type DestinationType `json:"type"`
	// ID is the saved destination's id. Empty for a presigned PUT.
	ID string `json:"id,omitempty"`
	// Name is the saved destination's label, absent if it has been deleted.
	Name string `json:"name,omitempty"`
}

// JobDelivery records the outcome of PUTting the result into your bucket.
type JobDelivery struct {
	// Status is pending, delivered or failed.
	Status DeliveryStatus `json:"status"`
	// Attempts is how many PUTs were made (up to 3).
	Attempts int `json:"attempts"`
	// DeliveredAt is an RFC 3339 timestamp, set once delivered.
	DeliveredAt string `json:"deliveredAt,omitempty"`
	// StatusCode is the bucket's HTTP status on the last attempt.
	StatusCode int `json:"statusCode,omitempty"`
	// Error explains a failed delivery.
	Error string `json:"error,omitempty"`
	// Key is the object key written, for a saved destination.
	Key string `json:"key,omitempty"`
	// Bucket is the bucket written to, for a saved destination.
	Bucket string `json:"bucket,omitempty"`
	// LocalCopyDeleted reports that snapnedit dropped its own copy after the
	// bucket confirmed the write (deleteAfterDelivery). The job's Download is
	// then nil.
	LocalCopyDeleted bool `json:"localCopyDeleted,omitempty"`
}

// JobStatus is the state half of a job, without the storage envelope. POST
// /jobs nests it under `status`; GET /jobs/{id} flattens it into the top level
// (see [Job]).
type JobStatus struct {
	// State is the lifecycle state.
	State JobState `json:"state"`
	// StartedAt is set while processing.
	StartedAt string `json:"startedAt,omitempty"`
	// OutputAssetID names the result asset. Set when succeeded.
	OutputAssetID string `json:"outputAssetId,omitempty"`
	// Download is a presigned URL for the result. Set when succeeded — except
	// after a deleteAfterDelivery delivery, where it is nil because the only
	// copy is the one in your bucket.
	Download *SignedURL `json:"download,omitempty"`
	// ErrorCode says why a failed job failed.
	ErrorCode ErrorCode `json:"errorCode,omitempty"`
	// Message is the human explanation for a failed job. Never parse it.
	Message string `json:"message,omitempty"`
}

// Terminal reports whether the job will not change state again.
func (s JobStatus) Terminal() bool {
	return s.State == JobSucceeded || s.State == JobFailed || s.State == JobCanceled
}

// Err returns an [*Error] for a failed or canceled job, and nil otherwise. A
// non-terminal or succeeded job is not an error.
func (s JobStatus) Err() error {
	switch s.State {
	case JobFailed:
		code := s.ErrorCode
		if code == "" {
			code = CodeInternal
		}
		return &Error{Code: code, Message: s.Message, Status: 200}
	case JobCanceled:
		return &Error{Code: CodeInternal, Message: "job was canceled", Status: 200}
	default:
		return nil
	}
}

// Job is what GET /jobs/{id} returns: a [JobStatus] flattened together with
// the bring-your-own-storage envelope.
type Job struct {
	JobStatus
	// Input says whether the job read an uploaded asset or an external URL.
	Input JobInput `json:"input"`
	// Destination summarizes where the result is delivered, or nil.
	Destination *JobDestinationSummary `json:"destination"`
	// Delivery records how that delivery went, or nil when there is none.
	Delivery *JobDelivery `json:"delivery"`
	// CreditCost is what this job was billed. 0 for a free operation, a cache
	// hit, or an unmetered caller.
	CreditCost int `json:"creditCost"`
	// Cached reports that the result came from the result cache rather than
	// from running a provider. A cache hit is never billed.
	Cached bool `json:"cached"`
	// DeliveryOnly marks a job that exists only to deliver an already-cached
	// result to a new destination.
	DeliveryOnly bool `json:"deliveryOnly"`
}

// Settled reports whether there is nothing left to wait for: the job is
// terminal AND, if it has a destination, the delivery is no longer pending.
//
// The second half matters for exactly one case — a cache hit with a
// destination. POST /jobs answers those already succeeded (the bytes existed)
// with the delivery still pending, because pushing them to your bucket is the
// worker's job and it has not run yet.
func (j *Job) Settled() bool {
	if !j.Terminal() {
		return false
	}
	return !(j.State == JobSucceeded && j.Delivery != nil && j.Delivery.Status == DeliveryPending)
}

// Destination is where the API delivers a job's result — bring your own
// storage, output half. Build one with [PresignedPutDestination] or
// [SavedDestination].
//
// Naming a destination requires an API key or embed token; an anonymous caller
// gets [CodeForbidden].
type Destination struct {
	// Type is "presigned-put" or "saved".
	Type DestinationType `json:"type"`
	// URL is the PUT you signed. Presigned destinations only.
	URL string `json:"url,omitempty"`
	// Headers are the headers your signature covers, sent verbatim on the PUT.
	// Only content-type, cache-control, content-disposition and x-amz-* /
	// x-goog-* / x-ms-* are accepted, at most 16.
	Headers map[string]string `json:"headers,omitempty"`
	// ID names a saved storage destination. Saved destinations only.
	ID string `json:"id,omitempty"`
}

// PresignedPutDestination delivers the result to a PUT URL you signed
// yourself. Pass the headers your signature covers, or nil.
func PresignedPutDestination(putURL string, headers map[string]string) *Destination {
	return &Destination{Type: DestinationPresignedPut, URL: putURL, Headers: headers}
}

// SavedDestination delivers the result to one of the account's saved storage
// destinations (see [DestinationsService]). The server signs the upload, so
// nothing about your bucket has to be in this process.
func SavedDestination(id string) *Destination {
	return &Destination{Type: DestinationSaved, ID: id}
}

// CreateJobOptions are the optional parts of [Client.CreateJob].
type CreateJobOptions struct {
	// Params are the operation's params. Nil means an empty object, which is
	// what every operation with no required params expects.
	Params Params
	// Destination delivers the result into your own bucket. Nil means "apply my
	// account's default destination, if I have one".
	Destination *Destination
	// NoDestination sends an explicit `destination: null`, opting this one job
	// out of the account default. It is NOT the same as leaving Destination
	// nil, and it wins if both are set.
	NoDestination bool
}

// CreateJobResult is what POST /jobs returns.
type CreateJobResult struct {
	// JobID identifies the job for [Client.GetJob] and [Client.WaitForJob].
	JobID string `json:"jobId"`
	// Status may already be terminal: an identical (operation, image, params)
	// triple is served from the result cache, free, without re-running a model.
	Status JobStatus `json:"status"`
	// Input says whether the job reads an uploaded asset or an external URL.
	Input JobInput `json:"input"`
	// Destination summarizes where the result will be delivered, or nil.
	Destination *JobDestinationSummary `json:"destination"`
	// Delivery records how that delivery went, or nil when there is none.
	Delivery *JobDelivery `json:"delivery"`
	// CreditCost is what this request was billed. 0 for a free operation, a
	// cache hit, or an unmetered caller.
	CreditCost int `json:"creditCost"`
	// Cached reports that the result came from the API's cache rather than
	// from running a provider — a 200 instead of a 202 — and was not billed.
	//
	// A cache hit still records its OWN job row, so JobID is a NEW id sharing
	// the earlier job's OutputAssetID. Never assert that the two ids match.
	Cached bool `json:"cached"`
	// DeliveryOnly marks a job that exists only to deliver an already-cached
	// result to a new destination.
	DeliveryOnly bool `json:"deliveryOnly"`
}

// Job re-assembles the status and the envelope into the same shape GET
// /jobs/{id} returns.
func (r *CreateJobResult) Job() *Job {
	return &Job{
		JobStatus:    r.Status,
		Input:        r.Input,
		Destination:  r.Destination,
		Delivery:     r.Delivery,
		CreditCost:   r.CreditCost,
		Cached:       r.Cached,
		DeliveryOnly: r.DeliveryOnly,
	}
}

// createJobBody is the wire body of POST /jobs. `destination` is a
// json.RawMessage so this client can distinguish "omitted" from "explicit
// null" — the two mean different things.
type createJobBody struct {
	Operation    Operation       `json:"operation"`
	InputAssetID string          `json:"inputAssetId,omitempty"`
	InputURL     string          `json:"inputUrl,omitempty"`
	Params       map[string]any  `json:"params"`
	Destination  json.RawMessage `json:"destination,omitempty"`
}

// CreateJob enqueues one operation — POST /jobs.
//
// input must be a [FromAsset] or [FromURL] input: this call does not move
// bytes. Upload them first with [Client.Upload], or use [Client.Run], which
// does the whole flow.
//
// The credits are debited here, at creation time, not at completion. A cache
// hit costs nothing and comes back already succeeded, with Cached set.
func (c *Client) CreateJob(ctx context.Context, operation Operation, input Input, opts *CreateJobOptions) (*CreateJobResult, error) {
	if err := c.requireKey("creating a job"); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &CreateJobOptions{}
	}

	body := createJobBody{Operation: operation, Params: paramsMapOf(opts.Params)}
	switch input.kind {
	case inputAsset:
		body.InputAssetID = input.assetID
	case inputURL:
		body.InputURL = input.url
	default:
		return nil, newError(CodeInvalidInput, 0, nil,
			"CreateJob needs an asset or url input — upload the bytes with Upload first, or call Run")
	}

	switch {
	case opts.NoDestination:
		body.Destination = json.RawMessage("null")
	case opts.Destination != nil:
		encoded, err := jsonBody(opts.Destination)
		if err != nil {
			return nil, err
		}
		body.Destination = encoded
	}

	encoded, err := jsonBody(body)
	if err != nil {
		return nil, err
	}

	resp, err := c.send(ctx, request{
		method:      http.MethodPost,
		path:        "/jobs",
		body:        encoded,
		contentType: "application/json",
		auth:        true,
		acceptJSON:  true,
	})
	if err != nil {
		return nil, err
	}
	var out CreateJobResult
	if err := json.Unmarshal(resp.body, &out); err != nil {
		return nil, newError(CodeInternal, resp.status, err, "POST /jobs returned a body this client could not parse")
	}
	// A 200 IS the cache hit, whatever the body says: the status code is the
	// older, narrower signal and an API that predates `cached` sends only it.
	out.Cached = out.Cached || resp.status == http.StatusOK
	return &out, nil
}

// GetJob reads one job — GET /jobs/{id}.
//
// It does not return an error for a job that failed: a failed job is a
// successful read. Check [JobStatus.Err] (or the State field) on the result.
//
// A job id that belongs to another account is [CodeNotFound], never
// [CodeForbidden] — an id must not become an existence oracle.
func (c *Client) GetJob(ctx context.Context, jobID string) (*Job, error) {
	if err := c.requireKey("reading a job"); err != nil {
		return nil, err
	}
	var job Job
	if err := c.doJSON(ctx, request{
		method: http.MethodGet,
		path:   "/jobs/" + url.PathEscape(jobID),
		auth:   true,
	}, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Default polling behaviour for [Client.WaitForJob] and [Client.Run].
const (
	DefaultPollInterval = time.Second
	DefaultPollTimeout  = 2 * time.Minute
)

// WaitOptions tune [Client.WaitForJob].
type WaitOptions struct {
	// PollInterval is the delay between GET /jobs/{id} calls. Defaults to
	// [DefaultPollInterval].
	PollInterval time.Duration
	// Timeout is the total budget before a [*TimeoutError]. Defaults to
	// [DefaultPollTimeout]. The context deadline, if earlier, still wins.
	Timeout time.Duration
}

// WaitForJob polls until the job settles — terminal, and with any delivery no
// longer pending — and returns it.
//
// Like [Client.GetJob] it does not turn a failed job into an error; check
// [JobStatus.Err]. It returns a [*TimeoutError] (which unwraps to [ErrTimeout])
// when the budget runs out first.
func (c *Client) WaitForJob(ctx context.Context, jobID string, opts *WaitOptions) (*Job, error) {
	interval := DefaultPollInterval
	timeout := DefaultPollTimeout
	if opts != nil {
		if opts.PollInterval > 0 {
			interval = opts.PollInterval
		}
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
	}
	deadline := time.Now().Add(timeout)

	for {
		job, err := c.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if job.Settled() {
			return job, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &TimeoutError{JobID: jobID, Timeout: timeout}
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, newError(CodeInternal, 0, ctx.Err(), "polling job %s was canceled", jobID)
		case <-timer.C:
		}
	}
}

// RunOptions tune [Client.Run].
type RunOptions struct {
	// Params are the operation's params. Nil means an empty object.
	Params Params
	// Mask is a second image for a mask-guided operation ([OpMagicEraser],
	// [OpGenerativeFill], [OpRemoveWatermark]). It is uploaded for you and set
	// as params.maskAssetId, so do not set that key yourself.
	Mask *Input
	// Destination delivers the result into your own bucket. Nil means "apply my
	// account's default destination, if I have one".
	Destination *Destination
	// NoDestination sends an explicit `destination: null`, opting this one job
	// out of the account default.
	NoDestination bool
	// PollInterval is the delay between status polls. Defaults to
	// [DefaultPollInterval].
	PollInterval time.Duration
	// Timeout is the total polling budget. Defaults to [DefaultPollTimeout].
	Timeout time.Duration
	// Download decides whether Run fetches the result bytes itself.
	//
	// Nil means the default: yes for an ordinary run, but NO when you named a
	// Destination and the server reports the delivery as delivered — the whole
	// point of a destination is that the bytes go straight to your bucket. A
	// delivery that FAILED still downloads, so you are never left
	// empty-handed. Point it at true or false to override either way.
	Download *bool
}

// RunResult is what [Client.Run] returns.
type RunResult struct {
	// JobID identifies the job that produced this result.
	JobID string
	// Job is the settled job, envelope included.
	Job *Job
	// Downloaded reports whether Output holds the result bytes.
	Downloaded bool
	// Output is the result image. Nil when Downloaded is false.
	Output []byte
	// MIME is Output's media type. Empty when Downloaded is false.
	MIME string
	// Download is a presigned URL for the result, whether or not Run fetched
	// it — nil when there is no copy left to sign one for, which happens after
	// a deleteAfterDelivery delivery.
	Download *SignedURL
	// Input says whether the job read an uploaded asset or an external URL.
	Input JobInput
	// Destination summarizes where the result was delivered, or nil.
	Destination *JobDestinationSummary
	// Delivery records how that delivery went, or nil when there was none.
	Delivery *JobDelivery
}

// Run is the whole flow in one call: upload the input (and the mask, if any),
// create the job, poll until it settles, and download the result.
//
// A [FromURL] input skips the upload entirely — the server fetches those bytes
// itself. A [RunOptions.Destination] has the server PUT the result straight
// into your bucket, and then the download is skipped by default and the result
// comes back with Downloaded false.
//
// It returns an [*Error] for a job that failed terminally (with the job's own
// [ErrorCode]) and a [*TimeoutError] if polling runs out of budget.
func (c *Client) Run(ctx context.Context, operation Operation, input Input, opts *RunOptions) (*RunResult, error) {
	if err := c.requireKey("running an operation"); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &RunOptions{}
	}

	ref := input
	if !input.IsURL() && input.kind != inputAsset {
		uploaded, err := c.Upload(ctx, input)
		if err != nil {
			return nil, err
		}
		ref = FromAsset(uploaded.AssetID)
	}

	params := paramsMapOf(opts.Params)
	if opts.Mask != nil {
		mask := *opts.Mask
		if mask.IsURL() {
			return nil, newError(CodeInvalidInput, 0, nil, "masks are asset-only — there is no url form")
		}
		if mask.kind == inputAsset {
			params[maskParamKey] = mask.assetID
		} else {
			uploaded, err := c.Upload(ctx, mask)
			if err != nil {
				return nil, err
			}
			params[maskParamKey] = uploaded.AssetID
		}
	}

	created, err := c.CreateJob(ctx, operation, ref, &CreateJobOptions{
		Params:        RawParams(params),
		Destination:   opts.Destination,
		NoDestination: opts.NoDestination,
	})
	if err != nil {
		return nil, err
	}

	settled := created.Job()
	if !settled.Settled() {
		waited, err := c.WaitForJob(ctx, created.JobID, &WaitOptions{
			PollInterval: opts.PollInterval,
			Timeout:      opts.Timeout,
		})
		if err != nil {
			return nil, err
		}
		settled = waited
	}

	if err := settled.Err(); err != nil {
		return nil, err
	}
	if settled.State != JobSucceeded {
		return nil, newError(CodeInternal, 0, nil,
			"job %s resolved to non-terminal state %q outside of polling", created.JobID, settled.State)
	}

	result := &RunResult{
		JobID:       created.JobID,
		Job:         settled,
		Download:    settled.Download,
		Input:       settled.Input,
		Destination: settled.Destination,
		Delivery:    settled.Delivery,
	}

	// Only an EXPLICIT destination flips the download default. An account
	// default applied server-side does not: a caller who wrote Run and nothing
	// else still expects bytes back, and the bucket copy is a bonus.
	askedForDelivery := opts.Destination != nil
	delivered := settled.Delivery != nil && settled.Delivery.Status == DeliveryDelivered
	shouldDownload := !(askedForDelivery && delivered)
	if opts.Download != nil {
		shouldDownload = *opts.Download
	}
	if !shouldDownload || settled.Download == nil {
		return result, nil
	}

	output, mime, err := c.Download(ctx, *settled.Download)
	if err != nil {
		return nil, err
	}
	result.Downloaded = true
	result.Output = output
	result.MIME = mime
	return result, nil
}
