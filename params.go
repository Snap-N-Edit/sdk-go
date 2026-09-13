package snapnedit

// Params is anything that can serialize itself to an operation's wire params
// object. Every typed `…Params` struct in this package implements it, and so
// does [RawParams] for a params shape this SDK version does not model yet.
//
// A nil Params serializes to an empty object, which is what every operation
// with no required params expects.
type Params interface {
	// ParamsMap returns the params exactly as they go into the `params` field
	// of POST /jobs. Zero-valued fields are omitted so the server applies its
	// own documented defaults.
	ParamsMap() map[string]any
}

// RawParams is an escape hatch: an untyped params object, sent verbatim. Use
// it for a param the typed structs below do not cover yet. The server's schema
// for every operation is strict, so an unrecognized key is a 400
// ([CodeInvalidInput]) rather than a silently ignored field.
type RawParams map[string]any

// ParamsMap implements [Params].
func (p RawParams) ParamsMap() map[string]any {
	if p == nil {
		return map[string]any{}
	}
	return map[string]any(p)
}

// paramsMapOf is nil-safe: a nil Params (or a nil interface holding a nil map)
// becomes an empty object.
func paramsMapOf(p Params) map[string]any {
	if p == nil {
		return map[string]any{}
	}
	m := p.ParamsMap()
	if m == nil {
		return map[string]any{}
	}
	return m
}

// putString adds key only when value is non-empty, so the server's own default
// applies for an unset field.
func putString(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// putInt adds key only when value is non-zero.
func putInt(m map[string]any, key string, value int) {
	if value != 0 {
		m[key] = value
	}
}

// ---------------------------------------------------------------------------
// Enumerated param values
// ---------------------------------------------------------------------------

// UpscaleFactor is how much [OpUpscale] enlarges an image. Note that the wire
// values are strings, not numbers.
type UpscaleFactor string

// The factors [OpUpscale] accepts. The server default is [Upscale2x].
const (
	Upscale2x UpscaleFactor = "2"
	Upscale4x UpscaleFactor = "4"
)

// StyleTransferStyle is a preset look for [OpStyleTransfer].
type StyleTransferStyle string

// The styles [OpStyleTransfer] accepts. The server default is [StyleVivid].
const (
	StyleVivid  StyleTransferStyle = "vivid"
	StylePastel StyleTransferStyle = "pastel"
	StyleMosaic StyleTransferStyle = "mosaic"
	StyleStorm  StyleTransferStyle = "storm"
)

// BeautifyAmount is the strength of [OpBeautify], as a wire string.
type BeautifyAmount string

// The amounts [OpBeautify] accepts. The server default is [BeautifyMedium].
const (
	BeautifyLight  BeautifyAmount = "0.3"
	BeautifyMedium BeautifyAmount = "0.6"
	BeautifyStrong BeautifyAmount = "0.9"
	BeautifyMax    BeautifyAmount = "1"
)

// GenerativeFillMode trades speed against quality for [OpGenerativeFill].
type GenerativeFillMode string

// The modes [OpGenerativeFill] accepts. The server default is [FillQuality].
const (
	FillFast    GenerativeFillMode = "fast"
	FillQuality GenerativeFillMode = "quality"
)

// DenoiseStrength is the strength of [OpAIDenoise], as a wire string.
type DenoiseStrength string

// The strengths [OpAIDenoise] accepts. The server default is [DenoiseMax].
const (
	DenoiseLight  DenoiseStrength = "0.25"
	DenoiseMedium DenoiseStrength = "0.5"
	DenoiseStrong DenoiseStrength = "0.75"
	DenoiseMax    DenoiseStrength = "1"
)

// SkyPreset is a replacement sky for [OpReplaceSky].
type SkyPreset string

// The skies [OpReplaceSky] accepts. The server default is [SkyBlue].
const (
	SkyBlue           SkyPreset = "blue-sky"
	SkySunset         SkyPreset = "sunset"
	SkyDramaticClouds SkyPreset = "dramatic-clouds"
	SkyGoldenHour     SkyPreset = "golden-hour"
	SkyNight          SkyPreset = "night"
	SkyOvercast       SkyPreset = "overcast"
)

// RelightDirection is where [OpRelight] puts the key light.
type RelightDirection string

// The directions [OpRelight] accepts. The server default is [RelightFront].
const (
	RelightLeft    RelightDirection = "left"
	RelightRight   RelightDirection = "right"
	RelightFront   RelightDirection = "front"
	RelightTop     RelightDirection = "top"
	RelightBacklit RelightDirection = "backlit"
)

// BackgroundPreset is a replacement background for [OpReplaceBackground].
type BackgroundPreset string

// The backgrounds [OpReplaceBackground] accepts. The server default is
// [BackgroundWhite].
const (
	BackgroundWhite      BackgroundPreset = "white"
	BackgroundBlack      BackgroundPreset = "black"
	BackgroundStudioGrey BackgroundPreset = "studio-grey"
	BackgroundStudioBlue BackgroundPreset = "studio-blue"
	BackgroundSunset     BackgroundPreset = "sunset"
	BackgroundOcean      BackgroundPreset = "ocean"
	BackgroundLavender   BackgroundPreset = "lavender"
)

// WatermarkStrength is how aggressively [OpAutoRemoveWatermark] works.
type WatermarkStrength string

// The strengths [OpAutoRemoveWatermark] accepts. The server default is
// [WatermarkMedium].
const (
	WatermarkLow    WatermarkStrength = "low"
	WatermarkMedium WatermarkStrength = "medium"
	WatermarkHigh   WatermarkStrength = "high"
)

// ResizeFit is how [OpResizeImage] fits an image into the requested box.
type ResizeFit string

// The fits [OpResizeImage] accepts. The server default is [FitInside].
const (
	// FitInside scales down to fit inside the box, preserving aspect ratio.
	FitInside ResizeFit = "inside"
	// FitCover fills the box and crops the overflow.
	FitCover ResizeFit = "cover"
	// FitFill stretches to the exact box, ignoring aspect ratio.
	FitFill ResizeFit = "fill"
)

// ImageFormat is an output encoding for [OpResizeImage].
type ImageFormat string

// The formats [OpResizeImage] accepts. The server default is [FormatPNG].
const (
	FormatPNG  ImageFormat = "png"
	FormatJPEG ImageFormat = "jpeg"
	FormatWebP ImageFormat = "webp"
)

// ResizeMaxDimension is the largest width or height [OpResizeImage] accepts.
const ResizeMaxDimension = 8192

// ---------------------------------------------------------------------------
// Typed params, one struct per operation
// ---------------------------------------------------------------------------

// RemoveBackgroundParams are the params for [OpRemoveBackground]. It takes none.
type RemoveBackgroundParams struct{}

// ParamsMap implements [Params].
func (p RemoveBackgroundParams) ParamsMap() map[string]any { return map[string]any{} }

// UpscaleParams are the params for [OpUpscale].
type UpscaleParams struct {
	// Factor is how much to enlarge. Defaults to [Upscale2x] server-side.
	Factor UpscaleFactor
}

// ParamsMap implements [Params].
func (p UpscaleParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "factor", string(p.Factor))
	return m
}

// UnblurParams are the params for [OpUnblur]. It takes none.
type UnblurParams struct{}

// ParamsMap implements [Params].
func (p UnblurParams) ParamsMap() map[string]any { return map[string]any{} }

// ColorizeParams are the params for [OpColorize]. It takes none.
type ColorizeParams struct{}

// ParamsMap implements [Params].
func (p ColorizeParams) ParamsMap() map[string]any { return map[string]any{} }

// StyleTransferParams are the params for [OpStyleTransfer].
type StyleTransferParams struct {
	// Style is the preset look. Defaults to [StyleVivid] server-side.
	Style StyleTransferStyle
}

// ParamsMap implements [Params].
func (p StyleTransferParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "style", string(p.Style))
	return m
}

// RetouchParams are the params for [OpRetouch]. It takes none.
type RetouchParams struct{}

// ParamsMap implements [Params].
func (p RetouchParams) ParamsMap() map[string]any { return map[string]any{} }

// BeautifyParams are the params for [OpBeautify].
type BeautifyParams struct {
	// Amount is the strength. Defaults to [BeautifyMedium] server-side.
	Amount BeautifyAmount
}

// ParamsMap implements [Params].
func (p BeautifyParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "amount", string(p.Amount))
	return m
}

// MagicEraserParams are the params for [OpMagicEraser], which needs a mask.
type MagicEraserParams struct {
	// MaskAssetID names an uploaded mask image (white = erase). [Client.Run]
	// fills this in for you when you pass [RunOptions.Mask].
	MaskAssetID string
}

// ParamsMap implements [Params].
func (p MagicEraserParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, maskParamKey, p.MaskAssetID)
	return m
}

// GenerativeFillParams are the params for [OpGenerativeFill], which needs both
// a mask and a prompt.
type GenerativeFillParams struct {
	// Prompt describes what to paint into the masked region. Required.
	Prompt string
	// Mode trades speed against quality. Defaults to [FillQuality] server-side.
	Mode GenerativeFillMode
	// MaskAssetID names an uploaded mask image. [Client.Run] fills this in for
	// you when you pass [RunOptions.Mask].
	MaskAssetID string
}

// ParamsMap implements [Params].
func (p GenerativeFillParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "prompt", p.Prompt)
	putString(m, "mode", string(p.Mode))
	putString(m, maskParamKey, p.MaskAssetID)
	return m
}

// RemoveWatermarkParams are the params for [OpRemoveWatermark], which needs a
// mask. For the maskless variant see [OpAutoRemoveWatermark].
type RemoveWatermarkParams struct {
	// MaskAssetID names an uploaded mask image covering the watermark.
	MaskAssetID string
}

// ParamsMap implements [Params].
func (p RemoveWatermarkParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, maskParamKey, p.MaskAssetID)
	return m
}

// AIDenoiseParams are the params for [OpAIDenoise].
type AIDenoiseParams struct {
	// Strength is how much noise to remove. Defaults to [DenoiseMax]
	// server-side.
	Strength DenoiseStrength
}

// ParamsMap implements [Params].
func (p AIDenoiseParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "strength", string(p.Strength))
	return m
}

// ReplaceSkyParams are the params for [OpReplaceSky].
type ReplaceSkyParams struct {
	// Sky is the replacement preset. Defaults to [SkyBlue] server-side.
	Sky SkyPreset
}

// ParamsMap implements [Params].
func (p ReplaceSkyParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "sky", string(p.Sky))
	return m
}

// RelightParams are the params for [OpRelight].
type RelightParams struct {
	// Direction is where the key light comes from. Defaults to [RelightFront]
	// server-side.
	Direction RelightDirection
}

// ParamsMap implements [Params].
func (p RelightParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "direction", string(p.Direction))
	return m
}

// ReplaceBackgroundParams are the params for [OpReplaceBackground].
type ReplaceBackgroundParams struct {
	// Background is the replacement preset. Defaults to [BackgroundWhite]
	// server-side.
	Background BackgroundPreset
}

// ParamsMap implements [Params].
func (p ReplaceBackgroundParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "background", string(p.Background))
	return m
}

// StripMetadataParams are the params for [OpStripMetadata]. It takes none.
type StripMetadataParams struct{}

// ParamsMap implements [Params].
func (p StripMetadataParams) ParamsMap() map[string]any { return map[string]any{} }

// AutoRemoveWatermarkParams are the params for [OpAutoRemoveWatermark].
type AutoRemoveWatermarkParams struct {
	// Strength is how aggressively to inpaint. Defaults to [WatermarkMedium]
	// server-side.
	Strength WatermarkStrength
}

// ParamsMap implements [Params].
func (p AutoRemoveWatermarkParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putString(m, "strength", string(p.Strength))
	return m
}

// ResizeImageParams are the params for [OpResizeImage], the one free
// operation. Give Width, Height, or both; the omitted one follows from Fit.
type ResizeImageParams struct {
	// Width in pixels, 1..[ResizeMaxDimension]. Omitted when zero.
	Width int
	// Height in pixels, 1..[ResizeMaxDimension]. Omitted when zero.
	Height int
	// Fit is how the image fills the box. Defaults to [FitInside] server-side.
	Fit ResizeFit
	// Format is the output encoding. Defaults to [FormatPNG] server-side.
	Format ImageFormat
	// Quality is the encoder quality, 1..100, for lossy formats. Defaults to 90
	// server-side. Omitted when zero.
	Quality int
}

// ParamsMap implements [Params].
func (p ResizeImageParams) ParamsMap() map[string]any {
	m := map[string]any{}
	putInt(m, "width", p.Width)
	putInt(m, "height", p.Height)
	putString(m, "fit", string(p.Fit))
	putString(m, "format", string(p.Format))
	putInt(m, "quality", p.Quality)
	return m
}
