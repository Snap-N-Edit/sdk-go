package snapnedit

import "context"

// Operation is the id of one snapnedit image operation. The 17 ids below are
// the complete catalog; [Client.ListOperations] fetches the live metadata for
// each of them.
type Operation string

// Every operation snapnedit publishes, in catalog order.
const (
	// OpRemoveBackground cuts the subject out of its background. Costs 1 credit.
	OpRemoveBackground Operation = "remove-background"
	// OpUpscale enlarges an image 2x or 4x. Costs 2 credits.
	OpUpscale Operation = "upscale"
	// OpUnblur sharpens a blurred photo. Costs 1 credit.
	OpUnblur Operation = "unblur"
	// OpColorize colorizes a black-and-white photo. Costs 1 credit.
	OpColorize Operation = "colorize"
	// OpStyleTransfer restyles a photo with a preset look. Costs 2 credits.
	OpStyleTransfer Operation = "style-transfer"
	// OpRetouch applies a general photo retouch. Costs 1 credit.
	OpRetouch Operation = "retouch"
	// OpBeautify applies portrait beautification. Costs 1 credit.
	OpBeautify Operation = "beautify"
	// OpMagicEraser erases whatever a mask covers. Requires a mask. Costs 2 credits.
	OpMagicEraser Operation = "magic-eraser"
	// OpGenerativeFill fills a masked region from a text prompt. Requires a
	// mask. Costs 3 credits.
	OpGenerativeFill Operation = "generative-fill"
	// OpRemoveWatermark removes a watermark a mask covers. Requires a mask.
	// Costs 2 credits.
	OpRemoveWatermark Operation = "remove-watermark"
	// OpAIDenoise removes sensor noise. Costs 1 credit.
	OpAIDenoise Operation = "ai-denoise"
	// OpReplaceSky replaces the sky with a preset. Costs 2 credits.
	OpReplaceSky Operation = "replace-sky"
	// OpRelight relights the subject from a chosen direction. Costs 2 credits.
	OpRelight Operation = "relight"
	// OpReplaceBackground swaps the background for a preset. Costs 2 credits.
	OpReplaceBackground Operation = "replace-background"
	// OpStripMetadata removes EXIF and other embedded metadata. Costs 1 credit.
	OpStripMetadata Operation = "strip-metadata"
	// OpAutoRemoveWatermark finds and removes a watermark without a mask.
	// Costs 2 credits.
	OpAutoRemoveWatermark Operation = "auto-remove-watermark"
	// OpResizeImage resizes, refits and re-encodes an image. Free.
	OpResizeImage Operation = "resize-image"
)

// Operations is every [Operation], in the order GET /operations returns them.
var Operations = []Operation{
	OpRemoveBackground,
	OpUpscale,
	OpUnblur,
	OpColorize,
	OpStyleTransfer,
	OpRetouch,
	OpBeautify,
	OpMagicEraser,
	OpGenerativeFill,
	OpRemoveWatermark,
	OpAIDenoise,
	OpReplaceSky,
	OpRelight,
	OpReplaceBackground,
	OpStripMetadata,
	OpAutoRemoveWatermark,
	OpResizeImage,
}

// creditCosts mirrors `x-credit-cost` in the published OpenAPI document.
var creditCosts = map[Operation]int{
	OpRemoveBackground:    1,
	OpUpscale:             2,
	OpUnblur:              1,
	OpColorize:            1,
	OpStyleTransfer:       2,
	OpRetouch:             1,
	OpBeautify:            1,
	OpMagicEraser:         2,
	OpGenerativeFill:      3,
	OpRemoveWatermark:     2,
	OpAIDenoise:           1,
	OpReplaceSky:          2,
	OpRelight:             2,
	OpReplaceBackground:   2,
	OpStripMetadata:       1,
	OpAutoRemoveWatermark: 2,
	OpResizeImage:         0,
}

// maskOperations is the set of operations whose params must carry a
// maskAssetId.
var maskOperations = map[Operation]bool{
	OpMagicEraser:     true,
	OpGenerativeFill:  true,
	OpRemoveWatermark: true,
}

// String returns the operation id as it goes on the wire.
func (o Operation) String() string { return string(o) }

// Valid reports whether o is one of the 17 published operations.
func (o Operation) Valid() bool { _, ok := creditCosts[o]; return ok }

// CreditCost is the fixed number of credits one run of this operation debits.
// Zero means free. A cache hit is never billed, and a job that fails
// terminally is refunded. Returns 0 for an unknown operation.
func (o Operation) CreditCost() int { return creditCosts[o] }

// RequiresMask reports whether this operation demands a mask asset id in its
// params. [Client.Run] uploads [RunOptions.Mask] and sets it for you.
func (o Operation) RequiresMask() bool { return maskOperations[o] }

// OperationMetadata is one entry of the live operation catalog returned by
// [Client.ListOperations].
type OperationMetadata struct {
	// ID is the operation id.
	ID Operation `json:"id"`
	// Label is the human-readable name.
	Label string `json:"label"`
	// Accept is the list of input MIME types this operation takes.
	Accept []string `json:"accept"`
	// MaxInputDimension is the longest edge, in pixels, an input may have
	// before the server downscales it.
	MaxInputDimension int `json:"maxInputDimension"`
	// ParamsJSONSchema is the JSON Schema for this operation's params, as
	// served. It is strict: an unrecognized key is a 400.
	ParamsJSONSchema map[string]any `json:"paramsJsonSchema"`
	// RequiresMask reports whether params.maskAssetId is mandatory.
	RequiresMask bool `json:"requiresMask"`
	// Description is marketing/help copy for the operation.
	Description string `json:"description"`
	// SEOTitle is the title used on the operation's own web page.
	SEOTitle string `json:"seoTitle"`
}

// ListOperations fetches the live operation catalog — GET /operations. It
// needs no credential.
//
// Prefer it over the compiled-in [Operations] list when you want the current
// params schema or the requiresMask flag: the catalog is the server's own
// answer, and it moves before this package does.
func (c *Client) ListOperations(ctx context.Context) ([]OperationMetadata, error) {
	var out []OperationMetadata
	if err := c.doJSON(ctx, request{method: "GET", path: "/operations", auth: true}, &out); err != nil {
		return nil, err
	}
	return out, nil
}
