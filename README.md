# snapnedit Go SDK

The official Go client for the [snapnedit](https://snapnedit.com) API — a self-hosted
AI photo-editing platform. Upload an image, run an operation, collect the result.

- **Standard library only.** No dependencies: `net/http`, `encoding/json`, `context`.
- **Context on every call**, with retries and backoff on 429/5xx/network for idempotent requests.
- **Typed end to end.** Operation ids, params, job states and error codes are real types, and
  every failure is an `*Error` you can match with `errors.Is` / `errors.As`.
- **Bring your own storage.** The server can fetch the input from a URL you name and PUT the
  result straight into your own bucket, so the bytes never pass through your process.

```sh
go get github.com/Snap-N-Edit/sdk-go
```

Requires Go 1.22 or newer.

## Quick start

```go
package main

import (
	"context"
	"log"
	"os"

	snapnedit "github.com/Snap-N-Edit/sdk-go"
)

func main() {
	client := snapnedit.NewClient(os.Getenv("SNAPNEDIT_API_KEY"))

	input, err := snapnedit.FromFile("photo.jpg")
	if err != nil {
		log.Fatal(err)
	}
	result, err := client.Run(context.Background(), snapnedit.OpRemoveBackground, input, nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("cutout.png", result.Output, 0o644); err != nil {
		log.Fatal(err)
	}
}
```

`Run` is the whole flow in one call: upload, create the job, poll until it settles,
download the result. Point the client somewhere else with `snapnedit.WithBaseURL("http://localhost:8787")`.

### The steps on their own

```go
uploaded, err := client.Upload(ctx, snapnedit.FromBytes(data, "image/png"))
created, err := client.CreateJob(ctx, snapnedit.OpUpscale, snapnedit.FromAsset(uploaded.AssetID),
	&snapnedit.CreateJobOptions{Params: snapnedit.UpscaleParams{Factor: snapnedit.Upscale4x}})
job, err := client.WaitForJob(ctx, created.JobID, nil)
bytes, mime, err := client.Download(ctx, *job.Download)
```

`created.Cached` reports that the API answered from its result cache — an identical
(operation, image, params) triple is served without re-running a model, and is never billed.
`GetJob` and `WaitForJob` do **not** turn a failed job into an error; check `job.Err()`.

### Inputs

| Constructor | What it is |
| --- | --- |
| `FromBytes(data, mime)` | An in-memory image. An empty `mime` is sniffed. |
| `FromReader(r, mime)` | A stream, read fully at upload time (the API needs the byte length up front). |
| `FromFile(path)` | An image on disk; the MIME comes from the extension. |
| `FromURL(url)` | A URL the **server** fetches — no upload happens at all. |
| `FromAsset(id)` | Something you already uploaded. |

### Typed params

One struct per operation, serializing to the wire shape. Zero-valued fields are omitted so the
server's own defaults apply — every operation's schema is strict, so an unrecognized key is a
`400`, never a silently ignored field.

```go
client.Run(ctx, snapnedit.OpResizeImage, input, &snapnedit.RunOptions{
	Params: snapnedit.ResizeImageParams{Width: 1200, Fit: snapnedit.FitInside, Format: snapnedit.FormatWebP, Quality: 82},
})
```

`snapnedit.RawParams{"factor": "4"}` is the escape hatch for anything not modelled yet.

A mask-guided operation takes a second image, uploaded for you and passed as `params.maskAssetId`:

```go
mask := snapnedit.FromFile("mask.png") // white = erase
result, err := client.Run(ctx, snapnedit.OpMagicEraser, photo, &snapnedit.RunOptions{Mask: &mask})
```

## Operations

| Operation | Constant | Credits | Params | Mask |
| --- | --- | --- | --- | --- |
| `remove-background` | `OpRemoveBackground` | 1 | — | |
| `upscale` | `OpUpscale` | 2 | `factor` `2`\|`4` | |
| `unblur` | `OpUnblur` | 1 | — | |
| `colorize` | `OpColorize` | 1 | — | |
| `style-transfer` | `OpStyleTransfer` | 2 | `style` vivid\|pastel\|mosaic\|storm | |
| `retouch` | `OpRetouch` | 1 | — | |
| `beautify` | `OpBeautify` | 1 | `amount` 0.3\|0.6\|0.9\|1 | |
| `magic-eraser` | `OpMagicEraser` | 2 | — | required |
| `generative-fill` | `OpGenerativeFill` | 3 | `prompt`, `mode` fast\|quality | required |
| `remove-watermark` | `OpRemoveWatermark` | 2 | — | required |
| `ai-denoise` | `OpAIDenoise` | 1 | `strength` 0.25\|0.5\|0.75\|1 | |
| `replace-sky` | `OpReplaceSky` | 2 | `sky` (6 presets) | |
| `relight` | `OpRelight` | 2 | `direction` (5 directions) | |
| `replace-background` | `OpReplaceBackground` | 2 | `background` (7 presets) | |
| `strip-metadata` | `OpStripMetadata` | 1 | — | |
| `auto-remove-watermark` | `OpAutoRemoveWatermark` | 2 | `strength` low\|medium\|high | |
| `resize-image` | `OpResizeImage` | **0** | `width`, `height`, `fit`, `format`, `quality` | |

`op.CreditCost()` and `op.RequiresMask()` read the table above; `client.ListOperations(ctx)`
fetches the live catalog, including each operation's current JSON Schema.

A cache hit is never billed, and a job that fails terminally is refunded.

## Bring your own storage

Two independent halves. The server can **fetch the input** from a URL you name, and **PUT the
result** into a bucket you name. Use both and nothing but JSON crosses the wire between you and
the API.

```go
result, err := client.Run(ctx, snapnedit.OpRemoveBackground, snapnedit.FromURL(presignedGetURL),
	&snapnedit.RunOptions{
		Destination: snapnedit.PresignedPutDestination(presignedPutURL,
			map[string]string{"content-type": "image/png"}),
	})

result.Downloaded // false — the bytes went straight to your bucket
result.Delivery   // { Status: "delivered", Attempts: 1, StatusCode: 200, DeliveredAt: … }
result.Download   // still a presigned URL on our side, if you want a copy after all
```

Naming a destination skips the download by default. Set `RunOptions.Download` to override
either way. A delivery that **failed** still downloads, so you are never left empty-handed —
`Status: "succeeded"` with `Delivery.Status: "failed"` is a real and correct combination.

The API never echoes the input URL, the destination URL or its headers back: they are bearer
credentials for your bucket.

### Saved storage destinations

Save an S3-compatible bucket once, then name it by id — or mark it as the account default and
name nothing at all. The server signs the upload, so your S3 credentials never enter this
process.

```go
yes := true
dest, err := client.Destinations.Create(ctx, snapnedit.StorageDestinationInput{
	Name: "exports", Provider: snapnedit.ProviderCloudflareR2,
	Bucket: "my-bucket", AccountID: "…", KeyPrefix: "snapnedit/",
	AccessKeyID: "…", SecretAccessKey: "…", IsDefault: &yes,
})

result, err := client.Run(ctx, snapnedit.OpUpscale, input,
	&snapnedit.RunOptions{Destination: snapnedit.SavedDestination(dest.ID)})
result.Delivery.Bucket // "my-bucket"
result.Delivery.Key    // "snapnedit/2026/09/13/<jobId>.png"
```

`List`, `Create`, `Update`, `Delete`, `Test` (a real write-then-delete probe against the bucket)
and `PresignUpload` (a one-shot signed PUT for something you produced yourself) are all on
`client.Destinations`.

Two shapes to know:

- **Omitting `Destination` is not the same as `NoDestination: true`.** Omitted means "apply my
  account default, if I have one"; `NoDestination` sends an explicit `null` that opts this one
  job out.
- A destination with `deleteAfterDelivery` drops snapnedit's own copy once your bucket confirms
  the write. The job then reports `Download == nil`, and `Run` returns `Downloaded: false`
  rather than an error.

## Webhooks

Register endpoint URLs and snapnedit POSTs a signed delivery every time one of your jobs
finishes. Verify the raw body **before** parsing it:

```go
import "github.com/Snap-N-Edit/sdk-go/webhooks"

func handler(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	event, err := webhooks.ConstructEvent(payload, r.Header.Get(webhooks.SignatureHeader),
		os.Getenv("SNAPNEDIT_WEBHOOK_SECRET"), webhooks.WithTolerance(5*time.Minute))
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case webhooks.EventJobSucceeded:
		log.Println(event.Data.JobID, event.Data.OutputAssetID, event.Data.Download)
	case webhooks.EventJobFailed:
		log.Println(event.Data.JobID, event.Data.ErrorCode, event.Data.Message)
	}
	w.WriteHeader(http.StatusOK)
}
```

The header is `X-Snapnedit-Signature: t=<unix seconds>,v1=<hex>`, and the signed string is
`"<t>.<raw body>"` HMAC-SHA256'd with the endpoint's secret. `v1` may appear more than once
during a secret rotation; any match is accepted. `webhooks.Verify` returns an error rather than
panicking for a malformed header, a bad signature or a stale timestamp, so every non-nil result
can be treated identically. `webhooks.Valid` is the same check as a `bool`.

## Errors

Every non-2xx response and every terminally failed job is an `*Error` carrying an `ErrorCode`.
Branch on the code, never on the message — messages are human text and change.

```go
result, err := client.Run(ctx, snapnedit.OpUpscale, input, nil)
switch {
case errors.Is(err, snapnedit.ErrPaymentRequired):
	// out of credits
case errors.Is(err, snapnedit.ErrRateLimited):
	// backed off three times already; slow down
case errors.Is(err, snapnedit.ErrTimeout):
	// polling ran out of budget; the job may still finish
case err != nil:
	var apiErr *snapnedit.Error
	if errors.As(err, &apiErr) {
		log.Printf("%s (http %d): %s", apiErr.Code, apiErr.Status, apiErr.Message)
	}
}
```

The closed set of codes, each with a matching `Err…` sentinel:

`invalid_input`, `unsupported_mime`, `too_large`, `not_found`, `input_fetch_failed`,
`provider_failed`, `provider_exhausted`, `rate_limited`, `bot_check_failed`, `unauthorized`,
`forbidden`, `payment_required`, `internal`.

A code this SDK version does not know degrades to `internal` rather than failing to parse.
A resource that belongs to another account is `not_found`, never `forbidden` — an id must not
become an existence oracle.

## Client options

```go
client := snapnedit.NewClient(apiKey,
	snapnedit.WithBaseURL("http://localhost:8787"),
	snapnedit.WithHTTPClient(&http.Client{Timeout: 90 * time.Second}),
	snapnedit.WithRetry(snapnedit.RetryConfig{MaxAttempts: 5, InitialBackoff: 500 * time.Millisecond, MaxBackoff: 10 * time.Second}),
	snapnedit.WithUserAgent("my-app/1.2.3"),
)
```

Retries apply to idempotent requests only (GET, HEAD, PUT, DELETE) on 429, 5xx and network
failures, with exponential backoff, jitter, and `Retry-After` honoured when the server sends it.
A POST that creates a job or a destination is **never** replayed. Pass a zero `RetryConfig` to
disable retries entirely.

`NewClient("")` builds an anonymous client, which can still reach the unauthenticated routes:
`ListOperations`, `Embed.CreateSession` and `Designs.Render`.

## Embedding the editor

`client.Embed.CreateSession(ctx, publishableKey, hostOrigin)` exchanges a `pk_` publishable key
for a short-lived editor token (no bearer header — the publishable key *is* the credential), and
`client.Embed.CreateToken(ctx, …)` mints a scoped one from your server with your `sk_` key. An
embed token authenticates as the account and is billed like an API key, so keep its TTL short.

## Testing

```sh
go test ./...                                              # unit tests, hermetic
SNAPNEDIT_CONFORMANCE=1 go test ./... -run Conformance -v   # the language-neutral suite
```

The conformance suite spawns the snapnedit monorepo's own server
(`node scripts/conformance-server.mjs`, the real API and the real worker over an in-memory
store), runs all 24 scenarios from `test/conformance/scenarios.json` as subtests named by their
ids, and fails if the file gains a scenario this client has not implemented. It needs the
monorepo alongside; set `SNAPNEDIT_REPO_ROOT` if it is not an ancestor of this directory.

## Links

- API reference — <https://snapnedit.com/docs/api-reference>
- OpenAPI document — <https://snapnedit.com/api/openapi.json>
- Storage destinations — <https://snapnedit.com/docs/storage-destinations>

---

This repository is **mirrored from the snapnedit monorepo**. Issues and pull requests are
welcome here; changes are landed upstream and mirrored back out.

MIT licensed — see [LICENSE](./LICENSE).
