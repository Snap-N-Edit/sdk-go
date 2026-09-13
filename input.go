package snapnedit

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Input names the image a job reads. Build one with [FromBytes],
// [FromReader], [FromFile], [FromURL] or [FromAsset]; the zero value is not
// usable.
//
// The first three carry bytes that this client uploads. [FromURL] carries a
// URL the SERVER fetches, so the bytes never pass through your process at all;
// [FromAsset] names something you already uploaded.
type Input struct {
	// kind is one of "bytes", "reader", "url", "asset".
	kind string
	// data holds the image for a bytes input.
	data []byte
	// reader streams the image for a reader input. Consumed once.
	reader io.Reader
	// mime is the declared media type of a bytes/reader input.
	mime string
	// url is the URL the server fetches for a url input.
	url string
	// assetID names an already-uploaded asset.
	assetID string
	// err defers a construction failure to the first call that uses the input.
	err error
}

// Input kinds.
const (
	inputBytes  = "bytes"
	inputReader = "reader"
	inputURL    = "url"
	inputAsset  = "asset"
)

// FromBytes wraps an in-memory image. Pass its media type — "image/png",
// "image/jpeg" or "image/webp". An empty mime is sniffed from the bytes.
func FromBytes(data []byte, mime string) Input {
	if mime == "" {
		mime = sniffMime(data)
	}
	return Input{kind: inputBytes, data: data, mime: mime}
}

// FromReader wraps a stream. The API needs the exact byte length up front to
// mint a presigned upload, so the whole stream is read into memory when the
// input is uploaded. An empty mime is sniffed from the leading bytes.
func FromReader(r io.Reader, mime string) Input {
	return Input{kind: inputReader, reader: r, mime: mime}
}

// FromFile reads an image off disk, taking its media type from the file
// extension and falling back to sniffing the content. It returns an Input
// carrying the error if the file cannot be read; the error surfaces from the
// call that uses the input, so this composes in one line:
//
//	result, err := client.Run(ctx, snapnedit.OpUpscale, snapnedit.Must(snapnedit.FromFile("in.png")), nil)
func FromFile(path string) (Input, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the caller's own
	if err != nil {
		return Input{kind: inputBytes, err: newError(CodeInvalidInput, 0, err, "could not read %s", path)}, err
	}
	mime := mimeFromExt(filepath.Ext(path))
	if mime == "" {
		mime = sniffMime(data)
	}
	return Input{kind: inputBytes, data: data, mime: mime}, nil
}

// FromURL names an image the SERVER fetches — bring your own storage, input
// half. Use a short-lived presigned GET, or any publicly reachable https URL.
//
// The API accepts https only, refuses private, loopback, link-local and
// metadata addresses, follows no redirects, times out at 30 seconds and stops
// reading at the same size ceiling uploads enforce. A fetch that fails for any
// of those reasons is a terminal job failure with code [CodeInputFetchFailed],
// and the credits are refunded.
//
// A URL input requires an API key or embed token: an anonymous caller gets
// [CodeForbidden].
func FromURL(rawURL string) Input {
	return Input{kind: inputURL, url: rawURL}
}

// FromAsset names an asset already uploaded with [Client.Upload], so a job can
// reuse it without moving the bytes again.
func FromAsset(assetID string) Input {
	return Input{kind: inputAsset, assetID: assetID}
}

// IsURL reports whether this input is a URL the server fetches rather than
// bytes this client uploads.
func (i Input) IsURL() bool { return i.kind == inputURL }

// MIME reports the input's declared media type, if it has one. A URL input has
// none: the server sniffs the type from the bytes it fetches.
func (i Input) MIME() string { return i.mime }

// bytes materializes a bytes/reader input, reading a reader exactly once.
func (i *Input) bytes() ([]byte, error) {
	if i.err != nil {
		return nil, i.err
	}
	switch i.kind {
	case inputBytes:
		return i.data, nil
	case inputReader:
		if i.reader == nil {
			return nil, newError(CodeInvalidInput, 0, nil, "input reader is nil")
		}
		data, err := io.ReadAll(i.reader)
		if err != nil {
			return nil, newError(CodeInvalidInput, 0, err, "could not read input")
		}
		// Cache, so an input is not silently empty if it is used twice.
		i.kind = inputBytes
		i.data = data
		i.reader = nil
		if i.mime == "" {
			i.mime = sniffMime(data)
		}
		return data, nil
	default:
		return nil, newError(CodeInvalidInput, 0, nil, "this input carries no bytes (kind %q)", i.kind)
	}
}

// mimeFromExt maps a file extension to the media type the API accepts for it.
func mimeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

// sniffMime detects a media type from the leading bytes, falling back to the
// generic binary type the API will refuse with [CodeUnsupportedMime] — which
// is the right outcome for something that is not an image.
func sniffMime(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	detected := http.DetectContentType(data)
	if idx := strings.IndexByte(detected, ';'); idx >= 0 {
		detected = detected[:idx]
	}
	return strings.TrimSpace(detected)
}

// Must unwraps a constructor that can fail, panicking on error. Intended for
// package-level fixtures and one-liners where a failure is a programming
// mistake, not a runtime condition.
func Must(input Input, err error) Input {
	if err != nil {
		panic(err)
	}
	return input
}
