package snapnedit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Snap-N-Edit/sdk-go/webhooks"
)

func TestGetAccount(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/account" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{
			"accountId": "a1", "email": nil, "plan": "free", "credits": 42,
			"key": map[string]any{"id": "k1", "mode": "test"},
		})
	}))
	me, err := c.GetAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.AccountID != "a1" || me.Email != nil || me.Credits != 42 || me.Key.Mode != KeyModeTest {
		t.Fatalf("account = %+v", me)
	}
}

func TestListJobsSendsTheQueryAndReadsTestLeniently(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/jobs" || q.Get("limit") != "20" || q.Get("status") != "succeeded" || q.Get("priority") != "" {
			t.Errorf("%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, map[string]any{"jobs": []any{
			map[string]any{"jobId": "j2", "createdAt": "2026-11-25T00:00:01Z", "state": "succeeded", "input": map[string]any{"kind": "asset"}, "test": true},
			map[string]any{"jobId": "j1", "createdAt": "2026-11-25T00:00:00Z", "state": "succeeded", "input": map[string]any{"kind": "asset"}},
		}})
	}))
	jobs, err := c.ListJobs(context.Background(), &ListJobsQuery{State: JobSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].JobID != "j2" || !jobs[0].Test || jobs[1].Test || jobs[1].State != JobSucceeded {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestCreateJobReadsTest(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 202, map[string]any{"jobId": "j1", "status": map[string]any{"state": "queued"}, "input": map[string]any{"kind": "asset"}, "test": true})
	}))
	created, err := c.CreateJob(context.Background(), OpUpscale, FromAsset("a1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Test || !created.Job().Test {
		t.Fatalf("created = %+v", created)
	}
}

func TestWebhookEndpointsLifecycle(t *testing.T) {
	endpoint := map[string]any{
		"id": "w1", "accountId": "a1", "url": "https://h.example/x",
		"events": []string{"job.succeeded", "session.ended"}, "active": true,
		"createdAt": "2026-11-25T00:00:00Z", "updatedAt": "2026-11-25T00:00:00Z",
	}
	var calls []string
	var bodies []map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		switch r.Method + " " + r.URL.Path {
		case "GET /webhook-endpoints":
			writeJSON(w, 200, map[string]any{"endpoints": []any{endpoint}})
		case "POST /webhook-endpoints":
			writeJSON(w, 201, map[string]any{"endpoint": endpoint, "secret": "whsec_1"})
		case "PATCH /webhook-endpoints/w1":
			paused := map[string]any{}
			for k, v := range endpoint {
				paused[k] = v
			}
			paused["active"] = false
			writeJSON(w, 200, map[string]any{"endpoint": paused})
		case "POST /webhook-endpoints/w1/rotate":
			writeJSON(w, 200, map[string]any{"secret": "whsec_2"})
		case "DELETE /webhook-endpoints/w1":
			w.WriteHeader(204)
		default:
			writeAPIError(w, 404, "not_found", "no such endpoint")
		}
	}))
	ctx := context.Background()
	created, err := c.WebhookEndpoints.Create(ctx, WebhookEndpointInput{URL: "https://h.example/x"})
	if err != nil || created.Secret != "whsec_1" || created.Endpoint.Events[1] != webhooks.EventSessionEnded {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	if _, present := bodies[0]["events"]; present {
		t.Fatal("unset events must be omitted so the server subscribes to all")
	}
	list, err := c.WebhookEndpoints.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "w1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	paused, err := c.WebhookEndpoints.SetActive(ctx, "w1", false)
	if err != nil || paused.Active || bodies[2]["active"] != false {
		t.Fatalf("SetActive = %+v, %v, body %v", paused, err, bodies[2])
	}
	secret, err := c.WebhookEndpoints.RotateSecret(ctx, "w1")
	if err != nil || secret != "whsec_2" {
		t.Fatalf("RotateSecret = %q, %v", secret, err)
	}
	if err := c.WebhookEndpoints.Delete(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := c.WebhookEndpoints.Delete(ctx, "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(gone) = %v", err)
	}
}
