package webhooks

import (
	"errors"
	"testing"
	"time"
)

// The fixed cross-language test vector from test/conformance/scenarios.json.
// Every snapnedit SDK's verifier must agree with it, with no live delivery
// involved.
const (
	vectorSecret    = "whsec_conformance_fixed_test_vector"
	vectorTimestamp = int64(1700000000)
	vectorRawBody   = `{"id":"whd_00000000-0000-4000-8000-000000000001","type":"job.succeeded","created":1700000000,"data":{"jobId":"9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d","operation":"remove-background","status":"succeeded","outputAssetId":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","download":"https://snapnedit.com/_local/results/abc.png?exp=1700003600&sig=deadbeef","input":{"kind":"asset"},"destination":null,"delivery":null}}`
	vectorSignature = "8a587f5869207b7da3be52c9d2695b0201c442c27cc37d2176551349e6461d90"
	vectorHeader    = "t=1700000000,v1=8a587f5869207b7da3be52c9d2695b0201c442c27cc37d2176551349e6461d90"
)

func TestSignReproducesTheFixedVector(t *testing.T) {
	if got := Sign([]byte(vectorRawBody), vectorTimestamp, vectorSecret); got != vectorSignature {
		t.Fatalf("Sign() = %s, want %s", got, vectorSignature)
	}
	if got := SignatureHeaderValue([]byte(vectorRawBody), vectorTimestamp, vectorSecret); got != vectorHeader {
		t.Fatalf("SignatureHeaderValue() = %s, want %s", got, vectorHeader)
	}
}

func TestVerifyAcceptsTheFixedVector(t *testing.T) {
	if err := Verify([]byte(vectorRawBody), vectorHeader, vectorSecret); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if !Valid([]byte(vectorRawBody), vectorHeader, vectorSecret) {
		t.Fatal("Valid() = false")
	}
}

func TestVerifyRefusals(t *testing.T) {
	// Flip the last hex character to produce a signature that is well formed
	// and wrong.
	tampered := vectorHeader[:len(vectorHeader)-1] + "b"
	if vectorHeader[len(vectorHeader)-1] == 'b' {
		tampered = vectorHeader[:len(vectorHeader)-1] + "a"
	}

	cases := []struct {
		name    string
		body    string
		header  string
		secret  string
		wantErr error
	}{
		{"wrong secret", vectorRawBody, vectorHeader, "not the secret", ErrSignatureMismatch},
		{"tampered signature", vectorRawBody, tampered, vectorSecret, ErrSignatureMismatch},
		{"body with a trailing space", vectorRawBody + " ", vectorHeader, vectorSecret, ErrSignatureMismatch},
		{"not a header at all", vectorRawBody, "not a signature header", vectorSecret, ErrInvalidHeader},
		{"no t=", vectorRawBody, "v1=" + vectorSignature, vectorSecret, ErrInvalidHeader},
		{"no v1=", vectorRawBody, "t=1700000000", vectorSecret, ErrInvalidHeader},
		{"empty header", vectorRawBody, "", vectorSecret, ErrInvalidHeader},
		{"non-numeric t", vectorRawBody, "t=soon,v1=" + vectorSignature, vectorSecret, ErrInvalidHeader},
		{"empty secret", vectorRawBody, vectorHeader, "", ErrEmptySecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify([]byte(tc.body), tc.header, tc.secret)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify() = %v, want %v", err, tc.wantErr)
			}
			if Valid([]byte(tc.body), tc.header, tc.secret) {
				t.Fatal("Valid() = true for a delivery that must be rejected")
			}
		})
	}
}

func TestVerifyToleranceWindow(t *testing.T) {
	signedAt := time.Unix(vectorTimestamp, 0)

	if err := Verify([]byte(vectorRawBody), vectorHeader, vectorSecret,
		WithTolerance(300*time.Second), WithNow(signedAt.Add(10*time.Second))); err != nil {
		t.Fatalf("10s inside a 300s window should pass, got %v", err)
	}
	if err := Verify([]byte(vectorRawBody), vectorHeader, vectorSecret,
		WithTolerance(300*time.Second), WithNow(signedAt.Add(3600*time.Second))); !errors.Is(err, ErrTimestampOutOfTolerance) {
		t.Fatalf("an hour outside a 300s window should fail, got %v", err)
	}
	// Drift in either direction is out of tolerance.
	if err := Verify([]byte(vectorRawBody), vectorHeader, vectorSecret,
		WithTolerance(300*time.Second), WithNow(signedAt.Add(-3600*time.Second))); !errors.Is(err, ErrTimestampOutOfTolerance) {
		t.Fatalf("a timestamp an hour in the future should fail, got %v", err)
	}
	// No tolerance option means no freshness check at all.
	if err := Verify([]byte(vectorRawBody), vectorHeader, vectorSecret, WithNow(signedAt.Add(10*365*24*time.Hour))); err != nil {
		t.Fatalf("without WithTolerance, age is irrelevant: %v", err)
	}
}

func TestVerifyAcceptsAnyV1DuringRotation(t *testing.T) {
	rotating := "t=1700000000,v1=" + Sign([]byte(vectorRawBody), vectorTimestamp, "an old secret") + ",v1=" + vectorSignature
	if err := Verify([]byte(vectorRawBody), rotating, vectorSecret); err != nil {
		t.Fatalf("any matching v1 should be accepted: %v", err)
	}
}

func TestVerifyIgnoresUnknownKeysAndIsCaseInsensitive(t *testing.T) {
	header := "t=1700000000, v0=legacy, v1=" + upper(vectorSignature) + " ,foo=bar"
	if err := Verify([]byte(vectorRawBody), header, vectorSecret); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
}

// upper is a tiny local helper so the test does not depend on strings.
func upper(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		}
	}
	return string(out)
}

func TestConstructEventParsesTheVector(t *testing.T) {
	event, err := ConstructEvent([]byte(vectorRawBody), vectorHeader, vectorSecret)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "whd_00000000-0000-4000-8000-000000000001" {
		t.Fatalf("id = %q", event.ID)
	}
	if event.Type != EventJobSucceeded {
		t.Fatalf("type = %q", event.Type)
	}
	if event.Created != vectorTimestamp || !event.CreatedAt().Equal(time.Unix(vectorTimestamp, 0).UTC()) {
		t.Fatalf("created = %d", event.Created)
	}
	if event.Data.JobID != "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d" || event.Data.Operation != "remove-background" {
		t.Fatalf("data = %+v", event.Data)
	}
	if event.Data.Status != "succeeded" || event.Data.OutputAssetID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" {
		t.Fatalf("data = %+v", event.Data)
	}
	if event.Data.Input.Kind != "asset" || event.Data.Destination != nil || event.Data.Delivery != nil {
		t.Fatalf("envelope = %+v", event.Data.JobEnvelope)
	}
}

func TestConstructEventNeverParsesAnUnverifiedPayload(t *testing.T) {
	if _, err := ConstructEvent([]byte(vectorRawBody), "t=1,v1=deadbeef", vectorSecret); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err = %v, want a signature mismatch before any parse", err)
	}
}

func TestConstructEventRejectsAVerifiedButUnparseableBody(t *testing.T) {
	body := []byte("not json")
	header := SignatureHeaderValue(body, vectorTimestamp, vectorSecret)
	if _, err := ConstructEvent(body, header, vectorSecret); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestFailedEventShape(t *testing.T) {
	body := []byte(`{"id":"whd_2","type":"job.failed","created":1700000000,"data":{"jobId":"j","operation":"upscale","status":"failed","errorCode":"provider_exhausted","message":"no provider","input":{"kind":"url"},"destination":{"type":"saved","id":"d1","name":"bucket"},"delivery":{"status":"failed","attempts":3,"error":"denied"}}}`)
	event, err := ConstructEvent(body, SignatureHeaderValue(body, vectorTimestamp, vectorSecret), vectorSecret)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != EventJobFailed || event.Data.ErrorCode != "provider_exhausted" || event.Data.Message != "no provider" {
		t.Fatalf("event = %+v", event)
	}
	if event.Data.Destination == nil || event.Data.Destination.ID != "d1" {
		t.Fatalf("destination = %+v", event.Data.Destination)
	}
	if event.Data.Delivery == nil || event.Data.Delivery.Attempts != 3 {
		t.Fatalf("delivery = %+v", event.Data.Delivery)
	}
}
