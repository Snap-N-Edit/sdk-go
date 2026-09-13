package snapnedit

import (
	"reflect"
	"testing"
)

func TestTypedParamsSerializeToTheWireShape(t *testing.T) {
	cases := []struct {
		name   string
		params Params
		want   map[string]any
	}{
		{"resize, everything", ResizeImageParams{Width: 800, Height: 600, Fit: FitCover, Format: FormatJPEG, Quality: 82},
			map[string]any{"width": 800, "height": 600, "fit": "cover", "format": "jpeg", "quality": 82}},
		{"resize, width only", ResizeImageParams{Width: 4}, map[string]any{"width": 4}},
		{"resize, zero value", ResizeImageParams{}, map[string]any{}},
		{"upscale", UpscaleParams{Factor: Upscale4x}, map[string]any{"factor": "4"}},
		{"upscale default", UpscaleParams{}, map[string]any{}},
		{"style transfer", StyleTransferParams{Style: StyleMosaic}, map[string]any{"style": "mosaic"}},
		{"beautify", BeautifyParams{Amount: BeautifyStrong}, map[string]any{"amount": "0.9"}},
		{"denoise", AIDenoiseParams{Strength: DenoiseLight}, map[string]any{"strength": "0.25"}},
		{"replace sky", ReplaceSkyParams{Sky: SkyGoldenHour}, map[string]any{"sky": "golden-hour"}},
		{"relight", RelightParams{Direction: RelightBacklit}, map[string]any{"direction": "backlit"}},
		{"replace background", ReplaceBackgroundParams{Background: BackgroundStudioBlue}, map[string]any{"background": "studio-blue"}},
		{"auto remove watermark", AutoRemoveWatermarkParams{Strength: WatermarkHigh}, map[string]any{"strength": "high"}},
		{"generative fill", GenerativeFillParams{Prompt: "a cat", Mode: FillFast, MaskAssetID: "m1"},
			map[string]any{"prompt": "a cat", "mode": "fast", "maskAssetId": "m1"}},
		{"magic eraser", MagicEraserParams{MaskAssetID: "m1"}, map[string]any{"maskAssetId": "m1"}},
		{"remove watermark", RemoveWatermarkParams{MaskAssetID: "m1"}, map[string]any{"maskAssetId": "m1"}},
		{"remove background", RemoveBackgroundParams{}, map[string]any{}},
		{"unblur", UnblurParams{}, map[string]any{}},
		{"colorize", ColorizeParams{}, map[string]any{}},
		{"retouch", RetouchParams{}, map[string]any{}},
		{"strip metadata", StripMetadataParams{}, map[string]any{}},
		{"raw", RawParams{"factor": "2"}, map[string]any{"factor": "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.params.ParamsMap(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParamsMap() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNilParamsIsAnEmptyObject(t *testing.T) {
	if got := paramsMapOf(nil); !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("paramsMapOf(nil) = %#v", got)
	}
	if got := paramsMapOf(RawParams(nil)); !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("paramsMapOf(nil RawParams) = %#v", got)
	}
}

func TestEveryTypedParamsStructHasAnOperation(t *testing.T) {
	// One typed struct per operation — a missing pair is a gap in the SDK.
	byOperation := map[Operation]Params{
		OpRemoveBackground:    RemoveBackgroundParams{},
		OpUpscale:             UpscaleParams{},
		OpUnblur:              UnblurParams{},
		OpColorize:            ColorizeParams{},
		OpStyleTransfer:       StyleTransferParams{},
		OpRetouch:             RetouchParams{},
		OpBeautify:            BeautifyParams{},
		OpMagicEraser:         MagicEraserParams{},
		OpGenerativeFill:      GenerativeFillParams{},
		OpRemoveWatermark:     RemoveWatermarkParams{},
		OpAIDenoise:           AIDenoiseParams{},
		OpReplaceSky:          ReplaceSkyParams{},
		OpRelight:             RelightParams{},
		OpReplaceBackground:   ReplaceBackgroundParams{},
		OpStripMetadata:       StripMetadataParams{},
		OpAutoRemoveWatermark: AutoRemoveWatermarkParams{},
		OpResizeImage:         ResizeImageParams{},
	}
	if len(byOperation) != len(Operations) {
		t.Fatalf("%d typed params structs for %d operations", len(byOperation), len(Operations))
	}
	for _, op := range Operations {
		if _, ok := byOperation[op]; !ok {
			t.Errorf("no typed params struct for %s", op)
		}
	}
}
