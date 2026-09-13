package snapnedit

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

// usageBody is a representative GET /usage response.
func usageBody() map[string]any {
	return map[string]any{
		"range":   map[string]any{"from": "2026-08-14T00:00:00.000Z", "to": "2026-09-13T23:59:59.999Z"},
		"groupBy": "operation",
		"totals": map[string]any{
			"jobs": 3, "credits": 4, "cacheHits": 1, "free": 1, "failed": 0,
			"delivered": 2, "deliveryFailed": 0, "sessions": 5, "activeSessions": 2,
		},
		"series": []map[string]any{{
			"key": "upscale", "label": "upscale", "jobs": 2, "credits": 4, "cacheHits": 1,
			"free": 0, "failed": 0, "delivered": 2, "deliveryFailed": 0, "sessions": 0,
		}},
		"keys": []map[string]any{{
			"id": "key-1", "name": "server", "kind": "secret",
			"dailyCreditLimit": nil, "usedToday": 4,
		}},
	}
}

// usageServer answers /usage with body and records the query it was called with.
func usageServer(t *testing.T, body map[string]any, status int) (*Client, *string) {
	t.Helper()
	seen := new(string)
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			t.Errorf("path = %s, want /usage", r.URL.Path)
		}
		*seen = r.URL.RawQuery
		writeJSON(w, status, body)
	}))
	return client, seen
}

func TestGetUsageParsesTheReport(t *testing.T) {
	client, seen := usageServer(t, usageBody(), 200)

	report, err := client.GetUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if *seen != "" {
		t.Fatalf("a nil query sent %q, want no query string", *seen)
	}
	if report.GroupBy != GroupByOperation {
		t.Fatalf("groupBy = %q", report.GroupBy)
	}
	if report.Range.From != "2026-08-14T00:00:00.000Z" {
		t.Fatalf("range.from = %q", report.Range.From)
	}
	if report.Totals.Jobs != 3 || report.Totals.Credits != 4 || report.Totals.CacheHits != 1 {
		t.Fatalf("totals = %+v", report.Totals)
	}
	if report.Totals.Sessions != 5 || report.Totals.ActiveSessions != 2 {
		t.Fatalf("session totals = %+v", report.Totals)
	}

	point := report.Point("upscale")
	if point == nil {
		t.Fatal("no upscale bucket")
	}
	if point.Credits != 4 || point.Delivered != 2 || point.Sessions != 0 {
		t.Fatalf("point = %+v", point)
	}
	if report.Point("resize-image") != nil {
		t.Fatal("Point returned a bucket that is not in the series")
	}

	if len(report.Keys) != 1 {
		t.Fatalf("keys = %+v", report.Keys)
	}
	if report.Keys[0].Kind != KeySecret || report.Keys[0].DailyCreditLimit != nil || report.Keys[0].UsedToday != 4 {
		t.Fatalf("key = %+v", report.Keys[0])
	}
}

func TestGetUsageBuildsTheQueryStringInWireSpelling(t *testing.T) {
	client, seen := usageServer(t, usageBody(), 200)

	_, err := client.GetUsage(context.Background(), &UsageQuery{
		From:      time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:        time.Date(2026, 9, 13, 14, 30, 0, 0, time.FixedZone("CEST", 2*60*60)),
		GroupBy:   GroupByOperation,
		KeyID:     "key-1",
		Origin:    "https://app.example",
		Operation: OpUpscale,
		Source:    SourceAPI,
	})
	if err != nil {
		t.Fatal(err)
	}

	values, err := url.ParseQuery(*seen)
	if err != nil {
		t.Fatal(err)
	}
	parsed := map[string]string{}
	for name, entries := range values {
		parsed[name] = entries[0]
	}
	want := map[string]string{
		"from":      "2026-09-01T00:00:00.000Z",
		"to":        "2026-09-13T12:30:00.000Z", // normalized to UTC
		"groupBy":   "operation",
		"keyId":     "key-1",
		"origin":    "https://app.example",
		"operation": "upscale",
		"source":    "api",
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("query = %v, want %v", parsed, want)
	}
}

func TestGetUsageOmitsZeroValuedFilters(t *testing.T) {
	client, seen := usageServer(t, usageBody(), 200)

	if _, err := client.GetUsage(context.Background(), &UsageQuery{GroupBy: GroupByDay}); err != nil {
		t.Fatal(err)
	}
	if *seen != "groupBy=day" {
		t.Fatalf("query = %q, want only groupBy=day", *seen)
	}
}

func TestGetUsageSurfacesTheApisInvalidInput(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, 400, "invalid_input", "from must not be after to")
	}))

	_, err := client.GetUsage(context.Background(), &UsageQuery{
		From: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestGetUsageNeedsACredential(t *testing.T) {
	client := NewClient("")
	if _, err := client.GetUsage(context.Background(), nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}
