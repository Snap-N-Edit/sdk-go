package snapnedit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// onePixelPNG is the smallest valid PNG, used to check MIME sniffing.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
}

func TestFromBytesSniffsAMissingMime(t *testing.T) {
	in := FromBytes(onePixelPNG, "")
	if in.MIME() != "image/png" {
		t.Fatalf("mime = %q", in.MIME())
	}
	if in.IsURL() {
		t.Fatal("a bytes input is not a url input")
	}
}

func TestFromReaderIsReadOnce(t *testing.T) {
	in := FromReader(bytes.NewReader(onePixelPNG), "")
	first, err := in.bytes()
	if err != nil {
		t.Fatal(err)
	}
	second, err := in.bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(onePixelPNG) || len(second) != len(onePixelPNG) {
		t.Fatalf("a reader input should cache its bytes, got %d then %d", len(first), len(second))
	}
	if in.MIME() != "image/png" {
		t.Fatalf("mime = %q", in.MIME())
	}
}

func TestFromFileTakesItsMimeFromTheExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := FromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if in.MIME() != "image/jpeg" {
		t.Fatalf("mime = %q, want the extension to win", in.MIME())
	}
}

func TestFromFileSniffsAnUnknownExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.bin")
	if err := os.WriteFile(path, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := FromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if in.MIME() != "image/png" {
		t.Fatalf("mime = %q", in.MIME())
	}
}

func TestFromFileMissing(t *testing.T) {
	if _, err := FromFile(filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestFromURLAndFromAsset(t *testing.T) {
	if !FromURL("https://cdn.example/x.png").IsURL() {
		t.Fatal("FromURL should be a url input")
	}
	if FromAsset("a1").IsURL() {
		t.Fatal("FromAsset is not a url input")
	}
	asset := FromAsset("a1")
	if _, err := asset.bytes(); err == nil {
		t.Fatal("an asset input carries no bytes")
	}
}
