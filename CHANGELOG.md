# Changelog

All notable changes to the snapnedit Go SDK are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-09-13

### Added

- `Client.GetUsage` (`GET /usage`): jobs, credits, cache hits, deliveries and
  embedded-editor sessions over a date range, bucketed by day, key, origin, operation or
  source.
- `UsageQuery` (`time.Time` range ends plus `KeyID` / `Origin` / `Operation` / `Source`
  filters), `UsageReport` with `Point`, `UsageRange`, `UsageFacts`, `UsageTotals`,
  `UsageSeriesPoint`, `UsageKeyRow`, the `UsageGroupBy`, `UsageSource` and `APIKeyKind`
  types with their constants, and `UsageUnattributed`.
- `CreditCost`, `Cached` and `DeliveryOnly` on `Job` and `CreateJobResult`, read from the
  job responses. `CreateJobResult.Cached` now prefers the API's own `cached` flag, falling
  back to "the API answered 200" for an older deployment.
- `usage.query` in the conformance suite, now 25 scenarios.

## [0.1.0] - 2026-09-13

First release. Standard library only — no dependencies.

### Added

- `NewClient(apiKey, ...Option)` with `WithBaseURL`, `WithHTTPClient`, `WithRetry` and
  `WithUserAgent`. Every call takes a `context.Context`.
- `Run` — upload, create, poll and download in one call — plus `Upload`, `CreateJob`, `GetJob`,
  `WaitForJob`, `Download` and `ListOperations` for driving the steps yourself.
- Inputs: `FromBytes`, `FromReader`, `FromFile`, `FromURL` and `FromAsset`.
- `Operation` constants for all 17 operations, with `CreditCost()` and `RequiresMask()`, and one
  typed params struct per operation (`ResizeImageParams`, `UpscaleParams`, …) serializing to the
  wire shape, plus `RawParams` as an escape hatch.
- Bring your own storage: `FromURL` inputs, `PresignedPutDestination`, `SavedDestination`, and
  `NoDestination` to opt out of an account default. `Run` skips the download when an explicit
  destination was delivered.
- `Destinations` service: `List`, `Create`, `Update`, `Delete`, `Test`, `PresignUpload`.
- `Embed` service: `CreateSession`, `CreateToken`.
- `Designs` service: `Create`, `CreatePages`, `Render`.
- Typed errors: `*Error{Code, Message, Status}` with `errors.Is`/`errors.As` support, an
  `ErrorCode` string type with every constant, one sentinel per code, and `*TimeoutError`
  unwrapping to `ErrTimeout`.
- Retries with exponential backoff, jitter and `Retry-After` support on 429/5xx/network for
  idempotent requests only.
- `webhooks` subpackage: `Verify`, `Valid`, `ConstructEvent`, `Sign`, `SignatureHeaderValue`,
  `WithTolerance`, `WithNow`, and typed `Event` / `EventData` shapes.
- Conformance suite covering every scenario in the monorepo's
  `test/conformance/scenarios.json`, gated behind `SNAPNEDIT_CONFORMANCE=1`.

[Unreleased]: https://github.com/Snap-N-Edit/sdk-go/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/Snap-N-Edit/sdk-go/releases/tag/v0.1.1
[0.1.0]: https://github.com/Snap-N-Edit/sdk-go/releases/tag/v0.1.0
