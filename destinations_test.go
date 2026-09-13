package snapnedit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

// destinationBody is a complete StorageDestination as the API sends it.
func destinationBody(id, name string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "provider": "s3-compatible", "bucket": "b",
		"region": "auto", "endpoint": "https://s3.example", "forcePathStyle": true,
		"keyPrefix": "p/", "accessKeyIdLast4": "KEY1", "isDefault": false,
		"deleteAfterDelivery": false, "lastTest": nil,
		"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
	}
}

func TestDestinationsList(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/destinations" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{"destinations": []any{destinationBody("d1", "one")}})
	}))
	list, err := c.Destinations.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "d1" || list[0].AccessKeyIDLast4 != "KEY1" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Region == nil || *list[0].Region != "auto" {
		t.Fatalf("region = %v", list[0].Region)
	}
}

func TestDestinationsCreateSendsTheInputVerbatim(t *testing.T) {
	var body map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 201, map[string]any{"destination": destinationBody("d1", "conformance")})
	}))
	yes := true
	dest, err := c.Destinations.Create(context.Background(), StorageDestinationInput{
		Name: "conformance", Provider: ProviderS3Compatible, Bucket: "b",
		Region: "auto", Endpoint: "https://s3.example", ForcePathStyle: &yes,
		KeyPrefix: "p/", AccessKeyID: "AKIAKEY1", SecretAccessKey: "shh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dest.ID != "d1" {
		t.Fatalf("dest = %+v", dest)
	}
	if body["provider"] != "s3-compatible" || body["forcePathStyle"] != true || body["secretAccessKey"] != "shh" {
		t.Fatalf("request body = %+v", body)
	}
	if _, present := body["accountId"]; present {
		t.Fatal("an unset accountId must be omitted, not sent empty")
	}
	if _, present := body["isDefault"]; present {
		t.Fatal("an unset isDefault must be omitted so the server keeps its own default")
	}
}

func TestDestinationPatchOmitsUnsetFieldsAndCanNull(t *testing.T) {
	name := "renamed"
	patch := StorageDestinationPatch{Name: &name, ClearRegion: true}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "renamed", "region": nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch = %#v, want %#v", got, want)
	}
}

func TestDestinationPatchClearBeatsAValue(t *testing.T) {
	endpoint := "https://ignored.example"
	encoded, _ := json.Marshal(StorageDestinationPatch{Endpoint: &endpoint, ClearEndpoint: true})
	var got map[string]any
	_ = json.Unmarshal(encoded, &got)
	if value, present := got["endpoint"]; !present || value != nil {
		t.Fatalf("endpoint = %#v, want an explicit null", value)
	}
}

func TestDestinationsUpdateAndDelete(t *testing.T) {
	var calls []string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		switch r.Method {
		case http.MethodPatch:
			writeJSON(w, 200, map[string]any{"destination": destinationBody("d 1", "renamed")})
		case http.MethodDelete:
			w.WriteHeader(204)
		}
	}))
	name := "renamed"
	dest, err := c.Destinations.Update(context.Background(), "d 1", StorageDestinationPatch{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if dest.Name != "renamed" {
		t.Fatalf("name = %q", dest.Name)
	}
	if err := c.Destinations.Delete(context.Background(), "d 1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATCH /destinations/d%201", "DELETE /destinations/d%201"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v (the id must be path-escaped)", calls, want)
	}
}

func TestDestinationsTestReportsAFailingBucketWithoutAnError(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "access denied", "latencyMs": 12.5})
	}))
	result, err := c.Destinations.Test(context.Background(), "d1")
	if err != nil {
		t.Fatalf("the HTTP call succeeded, so this must not be an error: %v", err)
	}
	if result.Ok || result.Error != "access denied" || result.LatencyMs != 12.5 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDestinationsPresignUpload(t *testing.T) {
	var body map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 200, map[string]any{
			"url": "https://s3.example/b/exports/2026/01/01/export-1-abcdef.png", "method": "PUT",
			"headers": map[string]string{"content-type": "image/png"},
			"key":     "exports/2026/01/01/export-1-abcdef.png", "bucket": "b", "expiresAt": "2030",
		})
	}))
	signed, err := c.Destinations.PresignUpload(context.Background(), "d1", PresignRequest{Ext: ExtPNG, ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if signed.Method != "PUT" || signed.Headers["content-type"] != "image/png" {
		t.Fatalf("signed = %+v", signed)
	}
	if body["ext"] != "png" || body["contentType"] != "image/png" {
		t.Fatalf("request = %+v", body)
	}
}

func TestForeignDestinationIs404(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 404, "not_found", "destination not found")
	}))
	_, err := c.Destinations.Update(context.Background(), "someone-elses", StorageDestinationPatch{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not_found (never forbidden)", err)
	}
}
