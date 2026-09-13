package snapnedit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// jobsMux is a stub of the upload + job + download flow, enough to drive Run
// end to end.
type jobsMux struct {
	t *testing.T
	// createBody records the decoded POST /jobs body.
	createBody map[string]any
	// putBody records the bytes PUT to the presigned upload url.
	putBody []byte
	// putContentType records the type declared on that PUT.
	putContentType string
	// job is what GET /jobs/{id} answers with, after polls has been reached.
	job map[string]any
	// early is what GET /jobs/{id} answers with before then.
	early map[string]any
	// polls is how many GETs return early before job is served.
	polls int32
	// gets counts GET /jobs/{id} calls.
	gets int32
	// createStatus is the status POST /jobs answers with.
	createStatus int
	// createResponse overrides the default POST /jobs body.
	createResponse map[string]any
	// downloads counts result downloads.
	downloads int32
}

func (m *jobsMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/uploads":
		writeJSON(w, 200, map[string]any{
			"assetId": "asset-1",
			"upload":  map[string]string{"url": "/_local/uploads/asset-1?sig=1", "expiresAt": "2030-01-01T00:00:00Z"},
		})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_local/uploads/"):
		body, _ := io.ReadAll(r.Body)
		m.putBody = body
		m.putContentType = r.Header.Get("Content-Type")
		w.WriteHeader(204)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/confirm"):
		writeJSON(w, 200, map[string]any{"assetId": "asset-1", "contentHash": strings.Repeat("a", 64), "bytes": len(m.putBody)})
	case r.Method == http.MethodPost && r.URL.Path == "/jobs":
		_ = json.NewDecoder(r.Body).Decode(&m.createBody)
		status := m.createStatus
		if status == 0 {
			status = 202
		}
		body := m.createResponse
		if body == nil {
			body = map[string]any{
				"jobId":       "job-1",
				"status":      map[string]any{"state": "queued"},
				"input":       map[string]string{"kind": "asset"},
				"destination": nil,
				"delivery":    nil,
			}
		}
		writeJSON(w, status, body)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/jobs/"):
		n := atomic.AddInt32(&m.gets, 1)
		if n <= m.polls && m.early != nil {
			writeJSON(w, 200, m.early)
			return
		}
		writeJSON(w, 200, m.job)
	case strings.HasPrefix(r.URL.Path, "/_local/results/"):
		atomic.AddInt32(&m.downloads, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("result-bytes"))
	default:
		m.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}
}

// succeededJob is the flattened GET /jobs/{id} body for a plain success.
func succeededJob() map[string]any {
	return map[string]any{
		"state":         "succeeded",
		"outputAssetId": "out-1",
		"download":      map[string]string{"url": "/_local/results/out-1.png?sig=2", "expiresAt": "2030-01-01T00:00:00Z"},
		"input":         map[string]string{"kind": "asset"},
		"destination":   nil,
		"delivery":      nil,
	}
}

func TestRunUploadsCreatesPollsAndDownloads(t *testing.T) {
	mux := &jobsMux{
		t:     t,
		job:   succeededJob(),
		early: map[string]any{"state": "processing", "startedAt": "now", "input": map[string]string{"kind": "asset"}},
		polls: 2,
	}
	c := testClient(t, mux)

	result, err := c.Run(context.Background(), OpUpscale, FromBytes([]byte("png-bytes"), "image/png"), &RunOptions{
		Params:       UpscaleParams{Factor: Upscale4x},
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || string(result.Output) != "result-bytes" || result.MIME != "image/png" {
		t.Fatalf("result = %+v", result)
	}
	if result.JobID != "job-1" {
		t.Fatalf("jobId = %q", result.JobID)
	}
	if string(mux.putBody) != "png-bytes" || mux.putContentType != "image/png" {
		t.Fatalf("upload PUT = %q / %q", mux.putBody, mux.putContentType)
	}
	if mux.createBody["operation"] != "upscale" || mux.createBody["inputAssetId"] != "asset-1" {
		t.Fatalf("create body = %+v", mux.createBody)
	}
	params, _ := mux.createBody["params"].(map[string]any)
	if !reflect.DeepEqual(params, map[string]any{"factor": "4"}) {
		t.Fatalf("params = %+v", params)
	}
	if _, present := mux.createBody["destination"]; present {
		t.Fatal("no destination was asked for, so the key must be omitted (that means: apply my default)")
	}
	if got := atomic.LoadInt32(&mux.gets); got != 3 {
		t.Fatalf("polls = %d, want 3", got)
	}
}

func TestRunWithURLInputSkipsUpload(t *testing.T) {
	mux := &jobsMux{t: t, job: map[string]any{
		"state":         "succeeded",
		"outputAssetId": "out-1",
		"download":      map[string]string{"url": "/_local/results/out-1.png", "expiresAt": "2030"},
		"input":         map[string]string{"kind": "url"},
		"destination":   nil,
		"delivery":      nil,
	}}
	c := testClient(t, mux)

	result, err := c.Run(context.Background(), OpRemoveBackground, FromURL("https://cdn.example/in.png"), &RunOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if mux.putBody != nil {
		t.Fatal("a url input must not upload anything")
	}
	if mux.createBody["inputUrl"] != "https://cdn.example/in.png" {
		t.Fatalf("create body = %+v", mux.createBody)
	}
	if _, present := mux.createBody["inputAssetId"]; present {
		t.Fatal("inputAssetId and inputUrl are mutually exclusive")
	}
	if result.Input.Kind != InputKindURL {
		t.Fatalf("input kind = %q", result.Input.Kind)
	}
}

func TestRunUploadsAMask(t *testing.T) {
	mux := &jobsMux{t: t, job: succeededJob()}
	c := testClient(t, mux)
	mask := FromBytes([]byte("mask"), "image/png")

	if _, err := c.Run(context.Background(), OpMagicEraser, FromBytes([]byte("photo"), "image/png"), &RunOptions{
		Mask:         &mask,
		PollInterval: time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	params, _ := mux.createBody["params"].(map[string]any)
	if params["maskAssetId"] != "asset-1" {
		t.Fatalf("params = %+v — the mask should have been uploaded and referenced", params)
	}
}

func TestRunRejectsAURLMask(t *testing.T) {
	mux := &jobsMux{t: t, job: succeededJob()}
	c := testClient(t, mux)
	mask := FromURL("https://cdn.example/mask.png")
	_, err := c.Run(context.Background(), OpMagicEraser, FromBytes([]byte("photo"), "image/png"), &RunOptions{Mask: &mask})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
}

func TestRunSkipsTheDownloadWhenAnExplicitDestinationWasDelivered(t *testing.T) {
	job := succeededJob()
	job["destination"] = map[string]any{"type": "presigned-put"}
	job["delivery"] = map[string]any{"status": "delivered", "attempts": 1, "deliveredAt": "2030", "statusCode": 200}
	mux := &jobsMux{t: t, job: job}
	c := testClient(t, mux)

	result, err := c.Run(context.Background(), OpRemoveBackground, FromBytes([]byte("photo"), "image/png"), &RunOptions{
		Destination:  PresignedPutDestination("https://bucket.example/k.png", map[string]string{"content-type": "image/png"}),
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded {
		t.Fatal("a delivered destination should skip the download by default")
	}
	if result.Download == nil {
		t.Fatal("the presigned download url should still be reported")
	}
	if atomic.LoadInt32(&mux.downloads) != 0 {
		t.Fatal("no bytes should have been pulled back through this process")
	}
	if result.Delivery == nil || result.Delivery.Status != DeliveryDelivered {
		t.Fatalf("delivery = %+v", result.Delivery)
	}
}

func TestRunStillDownloadsWhenDeliveryFailed(t *testing.T) {
	job := succeededJob()
	job["destination"] = map[string]any{"type": "presigned-put"}
	job["delivery"] = map[string]any{"status": "failed", "attempts": 3, "error": "bucket said no"}
	mux := &jobsMux{t: t, job: job}
	c := testClient(t, mux)

	result, err := c.Run(context.Background(), OpRemoveBackground, FromBytes([]byte("photo"), "image/png"), &RunOptions{
		Destination:  PresignedPutDestination("https://bucket.example/k.png", nil),
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded {
		t.Fatal("a failed delivery must not leave the caller empty-handed")
	}
}

func TestRunHandlesDeleteAfterDelivery(t *testing.T) {
	job := succeededJob()
	job["download"] = nil
	job["destination"] = map[string]any{"type": "saved", "id": "dst-1", "name": "my bucket"}
	job["delivery"] = map[string]any{"status": "delivered", "attempts": 1, "bucket": "b", "key": "k", "localCopyDeleted": true}
	c := testClient(t, &jobsMux{t: t, job: job})

	wantDownload := true
	result, err := c.Run(context.Background(), OpRemoveBackground, FromBytes([]byte("photo"), "image/png"), &RunOptions{
		Destination:  SavedDestination("dst-1"),
		Download:     &wantDownload,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("download: true against a deleted copy should not be an error, got %v", err)
	}
	if result.Downloaded || result.Download != nil {
		t.Fatalf("result = %+v — there is no copy left to download", result)
	}
	if result.Destination == nil || result.Destination.ID != "dst-1" {
		t.Fatalf("destination = %+v", result.Destination)
	}
}

func TestRunPollsACacheHitWhoseDeliveryIsStillPending(t *testing.T) {
	job := succeededJob()
	job["destination"] = map[string]any{"type": "saved", "id": "dst-1"}
	job["delivery"] = map[string]any{"status": "delivered", "attempts": 1, "bucket": "b", "key": "k"}
	mux := &jobsMux{
		t:            t,
		createStatus: 200,
		createResponse: map[string]any{
			"jobId":       "job-1",
			"status":      map[string]any{"state": "succeeded", "outputAssetId": "out-1", "download": map[string]string{"url": "/_local/results/out-1.png", "expiresAt": "2030"}},
			"input":       map[string]string{"kind": "asset"},
			"destination": map[string]any{"type": "saved", "id": "dst-1"},
			"delivery":    map[string]any{"status": "pending", "attempts": 0},
		},
		job: job,
	}
	c := testClient(t, mux)

	result, err := c.Run(context.Background(), OpRemoveBackground, FromBytes([]byte("photo"), "image/png"), &RunOptions{
		Destination:  SavedDestination("dst-1"),
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&mux.gets) == 0 {
		t.Fatal("a succeeded job with a pending delivery is not settled — it must be polled")
	}
	if result.Delivery.Status != DeliveryDelivered {
		t.Fatalf("delivery = %+v", result.Delivery)
	}
}

func TestRunReturnsATypedErrorForAFailedJob(t *testing.T) {
	c := testClient(t, &jobsMux{t: t, job: map[string]any{
		"state":       "failed",
		"errorCode":   "input_fetch_failed",
		"message":     "could not fetch the input url",
		"input":       map[string]string{"kind": "url"},
		"destination": nil,
		"delivery":    nil,
	}})
	_, err := c.Run(context.Background(), OpRemoveBackground, FromURL("https://cdn.example/missing.png"), &RunOptions{PollInterval: time.Millisecond})
	if !errors.Is(err, ErrInputFetchFailed) {
		t.Fatalf("err = %v, want input_fetch_failed", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Message != "could not fetch the input url" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestWaitForJobTimesOut(t *testing.T) {
	c := testClient(t, &jobsMux{t: t, job: map[string]any{"state": "queued", "input": map[string]string{"kind": "asset"}}})
	_, err := c.WaitForJob(context.Background(), "job-1", &WaitOptions{PollInterval: time.Millisecond, Timeout: 15 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.JobID != "job-1" {
		t.Fatalf("timeoutErr = %+v", timeoutErr)
	}
}

func TestGetJobDoesNotErrorOnAFailedJob(t *testing.T) {
	c := testClient(t, &jobsMux{t: t, job: map[string]any{
		"state": "failed", "errorCode": "provider_exhausted", "message": "no provider",
		"input": map[string]string{"kind": "asset"}, "destination": nil, "delivery": nil,
	}})
	job, err := c.GetJob(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("reading a failed job is a successful read: %v", err)
	}
	if job.State != JobFailed || job.ErrorCode != CodeProviderExhausted {
		t.Fatalf("job = %+v", job)
	}
	if !errors.Is(job.Err(), ErrProviderExhausted) {
		t.Fatalf("job.Err() = %v", job.Err())
	}
}

func TestCreateJobDestinationOmittedVersusNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts *CreateJobOptions
		want any
		key  bool
	}{
		{name: "omitted", opts: nil, key: false},
		{name: "explicit null", opts: &CreateJobOptions{NoDestination: true}, key: true, want: nil},
		{name: "saved", opts: &CreateJobOptions{Destination: SavedDestination("dst-1")}, key: true,
			want: map[string]any{"type": "saved", "id": "dst-1"}},
		{name: "presigned", opts: &CreateJobOptions{Destination: PresignedPutDestination("https://b/k", map[string]string{"content-type": "image/png"})}, key: true,
			want: map[string]any{"type": "presigned-put", "url": "https://b/k", "headers": map[string]any{"content-type": "image/png"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := &jobsMux{t: t}
			c := testClient(t, mux)
			if _, err := c.CreateJob(context.Background(), OpRemoveBackground, FromAsset("asset-1"), tc.opts); err != nil {
				t.Fatal(err)
			}
			got, present := mux.createBody["destination"]
			if present != tc.key {
				t.Fatalf("destination present = %v, want %v", present, tc.key)
			}
			if tc.key && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("destination = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCreateJobRejectsARawBytesInput(t *testing.T) {
	c := testClient(t, &jobsMux{t: t})
	_, err := c.CreateJob(context.Background(), OpRemoveBackground, FromBytes([]byte("x"), "image/png"), nil)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
}

func TestCreateJobReportsACacheHit(t *testing.T) {
	mux := &jobsMux{t: t, createStatus: 200, createResponse: map[string]any{
		"jobId":       "job-1",
		"status":      map[string]any{"state": "succeeded", "outputAssetId": "out-1", "download": map[string]string{"url": "/x", "expiresAt": "2030"}},
		"input":       map[string]string{"kind": "asset"},
		"destination": nil,
		"delivery":    nil,
	}}
	c := testClient(t, mux)
	created, err := c.CreateJob(context.Background(), OpRemoveBackground, FromAsset("asset-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Cached {
		t.Fatal("a 200 from POST /jobs is a cache hit")
	}
	if created.Status.State != JobSucceeded || !created.Job().Settled() {
		t.Fatalf("created = %+v", created)
	}
}

func TestJobSettledSemantics(t *testing.T) {
	pending := &Job{JobStatus: JobStatus{State: JobSucceeded}, Delivery: &JobDelivery{Status: DeliveryPending}}
	if pending.Settled() {
		t.Fatal("a succeeded job whose delivery is pending is not settled")
	}
	if !(&Job{JobStatus: JobStatus{State: JobSucceeded}}).Settled() {
		t.Fatal("a succeeded job with no destination is settled")
	}
	if (&Job{JobStatus: JobStatus{State: JobProcessing}}).Settled() {
		t.Fatal("a processing job is never settled")
	}
	if !(&Job{JobStatus: JobStatus{State: JobFailed}, Delivery: &JobDelivery{Status: DeliveryPending}}).Settled() {
		t.Fatal("a failed job is terminal regardless of delivery")
	}
}

func TestUploadRunsAllThreeSteps(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/uploads":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["mime"] != "image/png" || body["bytes"].(float64) != 5 {
				t.Errorf("upload request = %+v", body)
			}
			writeJSON(w, 200, map[string]any{"assetId": "a1", "upload": map[string]string{"url": "/_local/u/a1", "expiresAt": "2030"}})
		case strings.HasPrefix(r.URL.Path, "/_local/u/"):
			if r.Header.Get("Authorization") != "" {
				t.Error("a presigned PUT must not carry an Authorization header")
			}
			w.WriteHeader(204)
		default:
			writeJSON(w, 200, map[string]any{"assetId": "a1", "contentHash": strings.Repeat("b", 64), "bytes": 5})
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient("sk_live_test", WithBaseURL(srv.URL))
	out, err := c.Upload(context.Background(), FromBytes([]byte("bytes"), "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	if out.AssetID != "a1" || out.Bytes != 5 || len(out.ContentHash) != 64 {
		t.Fatalf("upload result = %+v", out)
	}
	want := []string{"POST /uploads", "PUT /_local/u/a1", "POST /uploads/a1/confirm"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("calls = %v, want %v", seen, want)
	}
}
