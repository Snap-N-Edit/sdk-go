package snapnedit

// The language-neutral SDK conformance suite, implemented in Go.
//
// It is env-gated so the default `go test ./...` stays a fast, hermetic unit
// run:
//
//	SNAPNEDIT_CONFORMANCE=1 go test ./... -run Conformance -v
//
// It spawns the real conformance server out of the snapnedit monorepo
// (`node scripts/conformance-server.mjs`, which boots the production Fastify
// app and the production worker over an in-memory store), reads the one line
// of JSON it prints when it is ready, runs every scenario in
// test/conformance/scenarios.json as a subtest named by its id, and SIGTERMs
// the server on the way out.
//
// Set SNAPNEDIT_REPO_ROOT if this package is checked out away from the
// monorepo.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Snap-N-Edit/sdk-go/webhooks"
)

// ---------------------------------------------------------------------------
// scenarios.json
// ---------------------------------------------------------------------------

// scenarioDoc is the parts of scenarios.json this suite reads.
type scenarioDoc struct {
	Version       int `json:"version"`
	WebhookVector struct {
		Secret               string `json:"secret"`
		Timestamp            int64  `json:"timestamp"`
		RawBody              string `json:"rawBody"`
		Signature            string `json:"signature"`
		SignatureHeaderValue string `json:"signatureHeaderValue"`
	} `json:"webhookVector"`
	Scenarios []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"scenarios"`
}

// ---------------------------------------------------------------------------
// The server under test
// ---------------------------------------------------------------------------

// conformanceServer is the ready line the server prints on stdout.
type conformanceServer struct {
	URL             string `json:"url"`
	APIKey          string `json:"apiKey"`
	PublishableKey  string `json:"publishableKey"`
	WebhookSecret   string `json:"webhookSecret"`
	AccountID       string `json:"accountId"`
	Bucket          string `json:"bucket"`
	StartingCredits int    `json:"startingCredits"`
}

// repoRoot locates the snapnedit monorepo — the directory holding
// scripts/conformance-server.mjs — by walking up from the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("SNAPNEDIT_REPO_ROOT"); override != "" {
		return override
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "scripts", "conformance-server.mjs")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the snapnedit repo root (scripts/conformance-server.mjs) — set SNAPNEDIT_REPO_ROOT")
		}
		dir = parent
	}
}

// startConformanceServer spawns the server on an ephemeral port and registers
// a cleanup that shuts it down.
func startConformanceServer(t *testing.T) (*conformanceServer, string) {
	t.Helper()
	root := repoRoot(t)

	cmd := exec.Command("node", filepath.Join("scripts", "conformance-server.mjs"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CONFORMANCE_PORT=0")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start the conformance server (is node on PATH?): %v", err)
	}

	var stopped atomic.Bool
	t.Cleanup(func() {
		if stopped.Swap(true) {
			return
		}
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	type ready struct {
		line string
		err  error
	}
	lines := make(chan ready, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		lines <- ready{line: line, err: err}
	}()

	select {
	case got := <-lines:
		if got.err != nil && strings.TrimSpace(got.line) == "" {
			t.Fatalf("the conformance server exited before printing its ready line: %v\nstderr:\n%s", got.err, stderr.String())
		}
		var server conformanceServer
		if err := json.Unmarshal([]byte(strings.TrimSpace(got.line)), &server); err != nil {
			t.Fatalf("could not parse the ready line %q: %v", got.line, err)
		}
		return &server, root
	case <-time.After(3 * time.Minute):
		t.Fatalf("the conformance server never became ready\nstderr:\n%s", stderr.String())
		return nil, ""
	}
}

// ---------------------------------------------------------------------------
// A raw HTTP client — deliberately not this SDK, so a scenario's wire
// assertions cannot be satisfied by machinery only a Go client happens to
// have. The SDK itself is exercised alongside, wherever a scenario maps onto
// one of its methods.
// ---------------------------------------------------------------------------

// rawClient speaks plain HTTP against the conformance server.
type rawClient struct {
	base  string
	token string
	hc    *http.Client
}

// httpResult is a fully-read raw response.
type httpResult struct {
	Status int
	Body   []byte
	Header http.Header
}

// Text is the response body as a string.
func (r *httpResult) Text() string { return string(r.Body) }

// Map decodes the body as a JSON object.
func (r *httpResult) Map(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("body is not a JSON object (%d): %s", r.Status, r.Text())
	}
	return out
}

// Slice decodes the body as a JSON array.
func (r *httpResult) Slice(t *testing.T) []any {
	t.Helper()
	var out []any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("body is not a JSON array (%d): %s", r.Status, r.Text())
	}
	return out
}

// ErrorCode reads error.code out of the uniform failure envelope, asserting the
// envelope has exactly the documented shape.
func (r *httpResult) ErrorCode(t *testing.T) string {
	t.Helper()
	body := r.Map(t)
	if len(body) != 1 {
		t.Fatalf("an error body must have exactly one top-level key, got %v", keysOf(body))
	}
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %s", r.Text())
	}
	if len(envelope) != 2 {
		t.Fatalf("error must be exactly { code, message }, got %v", keysOf(envelope))
	}
	message, _ := envelope["message"].(string)
	if message == "" {
		t.Fatalf("error.message must be a non-empty human string, got %s", r.Text())
	}
	code, _ := envelope["code"].(string)
	return code
}

// keysOf returns a map's sorted keys, for readable failure messages.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// do performs one request. A nil body sends none; a []byte body is sent raw;
// anything else is JSON-encoded.
func (c *rawClient) do(t *testing.T, method, path string, body any, headers map[string]string) *httpResult {
	t.Helper()
	target := path
	if !strings.HasPrefix(path, "http") {
		target = c.base + path
	}

	var reader io.Reader
	contentType := ""
	switch typed := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(context.Background(), method, target, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return &httpResult{Status: resp.StatusCode, Body: payload, Header: resp.Header}
}

// get, post, patch, del are thin wrappers.
func (c *rawClient) get(t *testing.T, path string) *httpResult {
	return c.do(t, http.MethodGet, path, nil, nil)
}
func (c *rawClient) post(t *testing.T, path string, body any) *httpResult {
	return c.do(t, http.MethodPost, path, body, nil)
}
func (c *rawClient) patch(t *testing.T, path string, body any) *httpResult {
	return c.do(t, http.MethodPatch, path, body, nil)
}
func (c *rawClient) del(t *testing.T, path string) *httpResult {
	return c.do(t, http.MethodDelete, path, nil, nil)
}

// ---------------------------------------------------------------------------
// The suite
// ---------------------------------------------------------------------------

// conformanceSuite carries everything the scenarios share.
type conformanceSuite struct {
	server *conformanceServer
	root   string
	doc    scenarioDoc
	// api is authenticated with the seeded account's secret key.
	api *rawClient
	// anon carries no credential at all.
	anon *rawClient
	// sdk is this package's own client, pointed at the same server.
	sdk *Client
	// implemented records which scenario ids ran.
	implemented map[string]bool
	// nonces keeps every generated nonce unique within a run.
	nonces atomic.Int64
}

// nonce produces a value nobody has submitted before, so a request cannot hit
// the result cache. Reusing one produces a mystifying "the balance did not
// change" failure.
func (s *conformanceSuite) nonce(label string) string {
	return fmt.Sprintf("go-%s-%d-%d", label, time.Now().UnixNano(), s.nonces.Add(1))
}

// scenario declares one test per scenario id, failing loudly if the id is not
// in scenarios.json.
func (s *conformanceSuite) scenario(t *testing.T, id string, fn func(t *testing.T)) {
	t.Helper()
	known := false
	for _, entry := range s.doc.Scenarios {
		if entry.ID == id {
			known = true
			break
		}
	}
	if !known {
		t.Fatalf("no scenario %q in scenarios.json", id)
	}
	s.implemented[id] = true
	t.Run(id, fn)
}

// fixture fetches real image bytes from the server's fixture helper.
func (s *conformanceSuite) fixture(t *testing.T, name, nonce string) []byte {
	t.Helper()
	path := "/__conformance/fixtures/" + name
	if nonce != "" {
		path += "?nonce=" + nonce
	}
	res := s.api.get(t, path)
	if res.Status != 200 {
		t.Fatalf("fixture %s: %d %s", name, res.Status, res.Text())
	}
	return res.Body
}

// upload runs the three-step upload flow with raw HTTP and returns the asset id.
func (s *conformanceSuite) upload(t *testing.T, data []byte, mime string) string {
	t.Helper()
	created := s.api.post(t, "/uploads", map[string]any{"mime": mime, "bytes": len(data)})
	if created.Status != 200 {
		t.Fatalf("POST /uploads: %d %s", created.Status, created.Text())
	}
	body := created.Map(t)
	assetID, _ := body["assetId"].(string)
	upload, _ := body["upload"].(map[string]any)
	uploadURL, _ := upload["url"].(string)

	put := s.api.do(t, http.MethodPut, uploadURL, data, map[string]string{"Content-Type": mime})
	if put.Status != 204 {
		t.Fatalf("presigned PUT: %d %s", put.Status, put.Text())
	}
	confirm := s.api.post(t, "/uploads/"+assetID+"/confirm", nil)
	if confirm.Status != 200 {
		t.Fatalf("confirm: %d %s", confirm.Status, confirm.Text())
	}
	return assetID
}

// uniqueUpload uploads a fixture nobody has submitted before.
func (s *conformanceSuite) uniqueUpload(t *testing.T, label string) string {
	t.Helper()
	return s.upload(t, s.fixture(t, "small.png", s.nonce(label)), "image/png")
}

// balance reads the seeded account's live credit balance.
func (s *conformanceSuite) balance(t *testing.T) float64 {
	t.Helper()
	res := s.api.get(t, "/__conformance/credits")
	if res.Status != 200 {
		t.Fatalf("credits helper: %d %s", res.Status, res.Text())
	}
	value, _ := res.Map(t)["balance"].(float64)
	return value
}

// poll drives GET /jobs/{id} until the job settles: terminal, and with any
// delivery no longer pending.
func (s *conformanceSuite) poll(t *testing.T, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		res := s.api.get(t, "/jobs/"+jobID)
		if res.Status != 200 {
			t.Fatalf("GET /jobs/%s: %d %s", jobID, res.Status, res.Text())
		}
		body := res.Map(t)
		state, _ := body["state"].(string)
		terminal := state == "succeeded" || state == "failed" || state == "canceled"
		pending := false
		if delivery, ok := body["delivery"].(map[string]any); ok {
			pending = delivery["status"] == "pending"
		}
		if terminal && !(state == "succeeded" && pending) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never settled: %s", jobID, res.Text())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// bucketObject reads one object back out of the stand-in customer bucket.
func (s *conformanceSuite) bucketObject(t *testing.T, key string) map[string]any {
	t.Helper()
	res := s.api.get(t, "/__conformance/bucket/"+key)
	if res.Status != 200 {
		return nil
	}
	return res.Map(t)
}

// createDestination saves the conformance server itself as an S3-compatible
// destination and returns its id.
func (s *conformanceSuite) createDestination(t *testing.T, overrides map[string]any) (string, map[string]any) {
	t.Helper()
	body := map[string]any{
		"name":            "conformance " + s.nonce("dest"),
		"provider":        "s3-compatible",
		"bucket":          s.server.Bucket,
		"region":          "auto",
		"endpoint":        s.server.URL,
		"forcePathStyle":  true,
		"keyPrefix":       "conformance/",
		"accessKeyId":     "CONFORMANCEKEYID",
		"secretAccessKey": "conformance-secret-access-key",
	}
	for key, value := range overrides {
		body[key] = value
	}
	res := s.api.post(t, "/destinations", body)
	if res.Status != 201 {
		t.Fatalf("POST /destinations: %d %s", res.Status, res.Text())
	}
	destination, _ := res.Map(t)["destination"].(map[string]any)
	id, _ := destination["id"].(string)
	return id, body
}

// TestConformance runs the whole language-neutral suite. It is skipped unless
// SNAPNEDIT_CONFORMANCE=1.
func TestConformance(t *testing.T) {
	if os.Getenv("SNAPNEDIT_CONFORMANCE") != "1" {
		t.Skip("set SNAPNEDIT_CONFORMANCE=1 to run the conformance suite against a spawned server")
	}

	server, root := startConformanceServer(t)
	raw, err := os.ReadFile(filepath.Join(root, "test", "conformance", "scenarios.json"))
	if err != nil {
		t.Fatalf("read scenarios.json: %v", err)
	}
	var doc scenarioDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse scenarios.json: %v", err)
	}

	hc := &http.Client{Timeout: 30 * time.Second}
	s := &conformanceSuite{
		server:      server,
		root:        root,
		doc:         doc,
		api:         &rawClient{base: server.URL, token: server.APIKey, hc: hc},
		anon:        &rawClient{base: server.URL, hc: hc},
		sdk:         NewClient(server.APIKey, WithBaseURL(server.URL), WithRetry(RetryConfig{})),
		implemented: map[string]bool{},
	}

	t.Logf("conformance server at %s (account %s, %d credits)", server.URL, server.AccountID, server.StartingCredits)

	/* ---------------- auth ---------------- */

	s.scenario(t, "auth.missing-credential", func(t *testing.T) {
		res := s.anon.get(t, "/destinations")
		if res.Status != 401 {
			t.Fatalf("status = %d, want 401", res.Status)
		}
		if code := res.ErrorCode(t); code != "unauthorized" {
			t.Fatalf("code = %q", code)
		}
	})

	s.scenario(t, "auth.bad-key", func(t *testing.T) {
		res := s.anon.do(t, http.MethodGet, "/destinations", nil,
			map[string]string{"Authorization": "Bearer sk_live_this_key_does_not_exist"})
		if res.Status != 401 {
			t.Fatalf("status = %d, want 401 (never 403)", res.Status)
		}
		if code := res.ErrorCode(t); code != "unauthorized" {
			t.Fatalf("code = %q", code)
		}
		if strings.Contains(res.Text(), s.server.AccountID) {
			t.Fatal("the response must not reveal whether any account exists")
		}
	})

	s.scenario(t, "auth.publishable-key-is-not-a-bearer", func(t *testing.T) {
		res := s.anon.do(t, http.MethodGet, "/destinations", nil,
			map[string]string{"Authorization": "Bearer " + s.server.PublishableKey})
		if res.Status != 401 {
			t.Fatalf("status = %d, want 401 — a pk_ key is only ever exchanged at POST /embed/sessions", res.Status)
		}
		if code := res.ErrorCode(t); code != "unauthorized" {
			t.Fatalf("code = %q", code)
		}
	})

	/* ---------------- catalog ---------------- */

	s.scenario(t, "operations.list", func(t *testing.T) {
		res := s.anon.get(t, "/operations")
		if res.Status != 200 {
			t.Fatalf("status = %d", res.Status)
		}
		entries := res.Slice(t)
		if len(entries) != 17 {
			t.Fatalf("%d operations, want 17", len(entries))
		}

		var ids []string
		var masked []string
		var upscaleSchema map[string]any
		for _, entry := range entries {
			op, _ := entry.(map[string]any)
			id, _ := op["id"].(string)
			ids = append(ids, id)
			for _, field := range []string{"id", "label", "accept", "maxInputDimension", "paramsJsonSchema", "requiresMask", "description", "seoTitle"} {
				if _, present := op[field]; !present {
					t.Fatalf("%s is missing %s", id, field)
				}
			}
			if accept, _ := op["accept"].([]any); len(accept) == 0 {
				t.Fatalf("%s accepts nothing", id)
			}
			if dim, _ := op["maxInputDimension"].(float64); dim <= 0 {
				t.Fatalf("%s maxInputDimension = %v", id, dim)
			}
			if op["requiresMask"] == true {
				masked = append(masked, id)
			}
			if id == "upscale" {
				upscaleSchema, _ = op["paramsJsonSchema"].(map[string]any)
			}
		}

		want := []string{
			"remove-background", "upscale", "unblur", "colorize", "style-transfer", "retouch",
			"beautify", "magic-eraser", "generative-fill", "remove-watermark", "ai-denoise",
			"replace-sky", "relight", "replace-background", "strip-metadata",
			"auto-remove-watermark", "resize-image",
		}
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Fatalf("ids = %v", ids)
		}
		sort.Strings(masked)
		if strings.Join(masked, ",") != "generative-fill,magic-eraser,remove-watermark" {
			t.Fatalf("mask-guided operations = %v", masked)
		}

		properties, _ := upscaleSchema["properties"].(map[string]any)
		factor, _ := properties["factor"].(map[string]any)
		enum, _ := factor["enum"].([]any)
		if len(enum) != 2 || enum[0] != "2" || enum[1] != "4" || factor["default"] != "2" {
			t.Fatalf("upscale factor schema = %v", factor)
		}

		// Credit costs are published on the OpenAPI document.
		spec := s.anon.get(t, "/openapi.json").Map(t)
		paths, _ := spec["paths"].(map[string]any)
		operations, _ := paths["/operations"].(map[string]any)
		get, _ := operations["get"].(map[string]any)
		costs, _ := get["x-credit-cost"].(map[string]any)
		if costs["resize-image"] != float64(0) || costs["remove-background"] != float64(1) || costs["upscale"] != float64(2) {
			t.Fatalf("x-credit-cost = %v", costs)
		}
		components, _ := spec["components"].(map[string]any)
		schemas, _ := components["schemas"].(map[string]any)
		resize, _ := schemas["OperationParams.resize-image"].(map[string]any)
		if resize["x-credit-cost"] != float64(0) {
			t.Fatalf("OperationParams.resize-image x-credit-cost = %v", resize["x-credit-cost"])
		}

		// The SDK's own catalog must agree with the live one.
		sdkOps, err := s.sdk.ListOperations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(sdkOps) != len(Operations) {
			t.Fatalf("the SDK compiles in %d operations but the server serves %d", len(Operations), len(sdkOps))
		}
		for i, op := range sdkOps {
			if op.ID != Operations[i] {
				t.Fatalf("operation %d = %s, SDK has %s", i, op.ID, Operations[i])
			}
			if op.RequiresMask != op.ID.RequiresMask() {
				t.Fatalf("%s requiresMask = %v, SDK says %v", op.ID, op.RequiresMask, op.ID.RequiresMask())
			}
			if cost, ok := costs[string(op.ID)].(float64); ok && int(cost) != op.ID.CreditCost() {
				t.Fatalf("%s costs %v, SDK says %d", op.ID, cost, op.ID.CreditCost())
			}
		}
	})

	/* ---------------- the core job flow ---------------- */

	s.scenario(t, "jobs.happy-path", func(t *testing.T) {
		data := s.fixture(t, "small.png", s.nonce("happy"))

		created := s.api.post(t, "/uploads", map[string]any{"mime": "image/png", "bytes": len(data)})
		if created.Status != 200 {
			t.Fatalf("POST /uploads = %d %s", created.Status, created.Text())
		}
		body := created.Map(t)
		assetID, _ := body["assetId"].(string)
		upload, _ := body["upload"].(map[string]any)
		if assetID == "" || upload["url"] == nil || upload["expiresAt"] == nil {
			t.Fatalf("upload response = %s", created.Text())
		}

		put := s.api.do(t, http.MethodPut, upload["url"].(string), data, map[string]string{"Content-Type": "image/png"})
		if put.Status != 204 {
			t.Fatalf("presigned PUT = %d %s", put.Status, put.Text())
		}

		confirm := s.api.post(t, "/uploads/"+assetID+"/confirm", nil)
		if confirm.Status != 200 {
			t.Fatalf("confirm = %d %s", confirm.Status, confirm.Text())
		}
		confirmed := confirm.Map(t)
		hash, _ := confirmed["contentHash"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
			t.Fatalf("contentHash = %q", hash)
		}
		if confirmed["bytes"] != float64(len(data)) {
			t.Fatalf("bytes = %v, want %d", confirmed["bytes"], len(data))
		}
		if confirmed["assetId"] != assetID {
			t.Fatalf("assetId = %v", confirmed["assetId"])
		}

		job := s.api.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputAssetId": assetID})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		jobBody := job.Map(t)
		status, _ := jobBody["status"].(map[string]any)
		if status["state"] != "queued" {
			t.Fatalf("status = %v (POST /jobs NESTS the status)", status)
		}
		input, _ := jobBody["input"].(map[string]any)
		if input["kind"] != "asset" || jobBody["destination"] != nil || jobBody["delivery"] != nil {
			t.Fatalf("envelope = %s", job.Text())
		}
		jobID, _ := jobBody["jobId"].(string)

		done := s.poll(t, jobID)
		// GET /jobs/{id} FLATTENS the status to the top level.
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v: %v", done["state"], done)
		}
		if output, _ := done["outputAssetId"].(string); output == "" {
			t.Fatalf("outputAssetId = %v", done["outputAssetId"])
		}
		download, _ := done["download"].(map[string]any)
		if download == nil || download["url"] == nil || download["expiresAt"] == nil {
			t.Fatalf("download = %v", done["download"])
		}

		fetched := s.api.get(t, download["url"].(string))
		if fetched.Status != 200 || len(fetched.Body) == 0 {
			t.Fatalf("download = %d, %d bytes", fetched.Status, len(fetched.Body))
		}
		if !strings.Contains(fetched.Header.Get("Content-Type"), "image/png") {
			t.Fatalf("content-type = %q", fetched.Header.Get("Content-Type"))
		}

		// The same flow through this SDK, in one call.
		result, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromBytes(s.fixture(t, "small.png", s.nonce("happy-sdk")), "image/png"),
			&RunOptions{PollInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if !result.Downloaded || len(result.Output) == 0 || !strings.Contains(result.MIME, "image/png") {
			t.Fatalf("sdk result = %+v", result)
		}
	})

	s.scenario(t, "jobs.params-validation", func(t *testing.T) {
		assetID := s.uniqueUpload(t, "params")

		bad := s.api.post(t, "/jobs", map[string]any{
			"operation": "upscale", "inputAssetId": assetID, "params": map[string]any{"factor": "3"},
		})
		if bad.Status != 400 {
			t.Fatalf("status = %d, want 400", bad.Status)
		}
		if code := bad.ErrorCode(t); code != "invalid_input" {
			t.Fatalf("code = %q", code)
		}

		good := s.api.post(t, "/jobs", map[string]any{
			"operation": "upscale", "inputAssetId": assetID, "params": map[string]any{"factor": "4"},
		})
		if good.Status != 202 {
			t.Fatalf("factor '4' should be accepted, got %d %s", good.Status, good.Text())
		}

		// The SDK surfaces the same failure as a typed error.
		_, err := s.sdk.CreateJob(context.Background(), OpUpscale, FromAsset(assetID),
			&CreateJobOptions{Params: RawParams{"factor": "3"}})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("sdk err = %v, want invalid_input", err)
		}
	})

	s.scenario(t, "jobs.unknown-param-rejected", func(t *testing.T) {
		assetID := s.uniqueUpload(t, "strict-params")
		res := s.api.post(t, "/jobs", map[string]any{
			"operation": "upscale", "inputAssetId": assetID,
			"params": map[string]any{"factor": "2", "nope": "x"},
		})
		if res.Status != 400 {
			t.Fatalf("status = %d, want 400 — every operation's params schema is strict", res.Status)
		}
		if code := res.ErrorCode(t); code != "invalid_input" {
			t.Fatalf("code = %q", code)
		}
	})

	/* ---------------- credits ---------------- */

	s.scenario(t, "jobs.free-operation", func(t *testing.T) {
		before := s.balance(t)
		assetID := s.uniqueUpload(t, "free-op")

		job := s.api.post(t, "/jobs", map[string]any{
			"operation": "resize-image", "inputAssetId": assetID, "params": map[string]any{"width": 4},
		})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		done := s.poll(t, job.Map(t)["jobId"].(string))
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v", done["state"])
		}
		if after := s.balance(t); after != before {
			t.Fatalf("balance moved %v -> %v; resize-image costs 0", before, after)
		}
		if OpResizeImage.CreditCost() != 0 {
			t.Fatalf("the SDK's own cost table says resize-image costs %d", OpResizeImage.CreditCost())
		}
	})

	s.scenario(t, "jobs.credit-debit", func(t *testing.T) {
		before := s.balance(t)
		assetID := s.uniqueUpload(t, "debit")

		job := s.api.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputAssetId": assetID})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		if created := job.Map(t); created["cached"] != false || created["creditCost"] != float64(1) {
			t.Fatalf("job reports cached=%v creditCost=%v, want false/1", created["cached"], created["creditCost"])
		}
		// The debit lands at creation time, before the worker ever sees the job.
		if atCreate := s.balance(t); atCreate != before-1 {
			t.Fatalf("balance after creation = %v, want %v", atCreate, before-1)
		}
		done := s.poll(t, job.Map(t)["jobId"].(string))
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v", done["state"])
		}
		if after := s.balance(t); after != before-1 {
			t.Fatalf("balance after completion = %v, want %v", after, before-1)
		}
	})

	s.scenario(t, "jobs.cache-hit", func(t *testing.T) {
		assetID := s.uniqueUpload(t, "cache")

		first := s.api.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputAssetId": assetID})
		if first.Status != 202 {
			t.Fatalf("first POST /jobs = %d %s", first.Status, first.Text())
		}
		done := s.poll(t, first.Map(t)["jobId"].(string))
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v", done["state"])
		}
		outputAssetID, _ := done["outputAssetId"].(string)

		before := s.balance(t)
		second := s.api.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputAssetId": assetID})
		if second.Status != 200 {
			t.Fatalf("second POST /jobs = %d, want 200 (a cache hit, not 202)", second.Status)
		}
		body := second.Map(t)
		status, _ := body["status"].(map[string]any)
		if status["state"] != "succeeded" {
			t.Fatalf("cached status = %v", status)
		}
		if body["cached"] != true || body["creditCost"] != float64(0) {
			t.Fatalf("cached job reports cached=%v creditCost=%v", body["cached"], body["creditCost"])
		}
		// A cache hit records its OWN job row, so the id is new and only the
		// result is shared. Never assert the two ids match.
		if body["jobId"] == first.Map(t)["jobId"] {
			t.Fatal("a cache hit must name a new job id, not the cached job's")
		}
		if status["outputAssetId"] != outputAssetID {
			t.Fatalf("cached outputAssetId = %v, want %v", status["outputAssetId"], outputAssetID)
		}
		if after := s.balance(t); after != before {
			t.Fatalf("balance moved %v -> %v; a cache hit is never billed", before, after)
		}

		// The SDK reports the same hit through CreateJobResult.Cached.
		created, err := s.sdk.CreateJob(context.Background(), OpRemoveBackground, FromAsset(assetID), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !created.Cached || created.Status.State != JobSucceeded || created.CreditCost != 0 {
			t.Fatalf("sdk created = %+v", created)
		}
	})

	/* ---------------- bring your own storage ---------------- */

	s.scenario(t, "jobs.url-input", func(t *testing.T) {
		inputURL := s.server.URL + "/__conformance/fixtures/small.png?nonce=" + s.nonce("url-input")

		refused := s.anon.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputUrl": inputURL})
		if refused.Status != 403 {
			t.Fatalf("anonymous inputUrl = %d, want 403", refused.Status)
		}
		if code := refused.ErrorCode(t); code != "forbidden" {
			t.Fatalf("code = %q", code)
		}

		job := s.api.post(t, "/jobs", map[string]any{"operation": "remove-background", "inputUrl": inputURL})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		input, _ := job.Map(t)["input"].(map[string]any)
		if input["kind"] != "url" {
			t.Fatalf("input = %v", input)
		}
		if strings.Contains(job.Text(), "nonce=") {
			t.Fatal("the input url must never be echoed — it can be a bearer credential")
		}

		done := s.poll(t, job.Map(t)["jobId"].(string))
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v: %v", done["state"], done)
		}
		if doneInput, _ := done["input"].(map[string]any); doneInput["kind"] != "url" {
			t.Fatalf("GET reports input = %v", doneInput)
		}
		download, _ := done["download"].(map[string]any)
		fetched := s.api.get(t, download["url"].(string))
		if fetched.Status != 200 || len(fetched.Body) == 0 {
			t.Fatalf("download = %d, %d bytes", fetched.Status, len(fetched.Body))
		}

		// And through the SDK's own FromURL input.
		result, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromURL(s.server.URL+"/__conformance/fixtures/small.png?nonce="+s.nonce("url-input-sdk")),
			&RunOptions{PollInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if result.Input.Kind != InputKindURL || !result.Downloaded {
			t.Fatalf("sdk result = %+v", result)
		}
	})

	s.scenario(t, "jobs.url-input-fetch-failed", func(t *testing.T) {
		before := s.balance(t)
		job := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background",
			"inputUrl":  s.server.URL + "/__conformance/fixtures/does-not-exist.png",
		})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d, want 202 — the url is only fetched by the worker", job.Status)
		}
		done := s.poll(t, job.Map(t)["jobId"].(string))
		if done["state"] != "failed" {
			t.Fatalf("state = %v", done["state"])
		}
		if done["errorCode"] != "input_fetch_failed" {
			t.Fatalf("errorCode = %v", done["errorCode"])
		}
		if after := s.balance(t); after != before {
			t.Fatalf("balance %v -> %v; a terminal failure is refunded", before, after)
		}

		// The SDK turns the failed job into a typed error carrying that code.
		_, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromURL(s.server.URL+"/__conformance/fixtures/also-missing.png"),
			&RunOptions{PollInterval: 50 * time.Millisecond})
		if !errors.Is(err, ErrInputFetchFailed) {
			t.Fatalf("sdk err = %v, want input_fetch_failed", err)
		}
	})

	s.scenario(t, "jobs.presigned-destination", func(t *testing.T) {
		key := "presigned/" + s.nonce("put") + ".png"
		assetID := s.uniqueUpload(t, "presigned-dest")

		job := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": assetID,
			"destination": map[string]any{
				"type":    "presigned-put",
				"url":     s.server.URL + "/__conformance/bucket/" + key,
				"headers": map[string]string{"content-type": "image/png"},
			},
		})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		destination, _ := job.Map(t)["destination"].(map[string]any)
		if len(destination) != 1 || destination["type"] != "presigned-put" {
			t.Fatalf("destination = %v — the url and headers are secrets in motion", destination)
		}
		if strings.Contains(job.Text(), "__conformance/bucket") {
			t.Fatal("the destination url must never be echoed")
		}

		done := s.poll(t, job.Map(t)["jobId"].(string))
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v: %v", done["state"], done)
		}
		delivery, _ := done["delivery"].(map[string]any)
		if delivery["status"] != "delivered" {
			t.Fatalf("delivery = %v", delivery)
		}
		if attempts, _ := delivery["attempts"].(float64); attempts < 1 {
			t.Fatalf("attempts = %v", delivery["attempts"])
		}
		if _, ok := delivery["deliveredAt"].(string); !ok {
			t.Fatalf("deliveredAt = %v", delivery["deliveredAt"])
		}
		if done["download"] == nil {
			t.Fatal("a presigned-put delivery does not remove our copy")
		}

		object := s.bucketObject(t, key)
		if object == nil {
			t.Fatal("nothing was written to the bucket")
		}
		if size, _ := object["bytes"].(float64); size <= 0 {
			t.Fatalf("object bytes = %v", object["bytes"])
		}
		if object["contentType"] != "image/png" {
			t.Fatalf("object contentType = %v", object["contentType"])
		}

		// The SDK skips the download when the delivery succeeded.
		sdkKey := "presigned/" + s.nonce("put-sdk") + ".png"
		result, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromBytes(s.fixture(t, "small.png", s.nonce("presigned-sdk")), "image/png"),
			&RunOptions{
				Destination: PresignedPutDestination(s.server.URL+"/__conformance/bucket/"+sdkKey,
					map[string]string{"content-type": "image/png"}),
				PollInterval: 50 * time.Millisecond,
			})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if result.Downloaded {
			t.Fatal("a delivered destination should skip the download")
		}
		if result.Delivery == nil || result.Delivery.Status != DeliveryDelivered {
			t.Fatalf("sdk delivery = %+v", result.Delivery)
		}
		if s.bucketObject(t, sdkKey) == nil {
			t.Fatal("the SDK's own run did not reach the bucket")
		}
	})

	/* ---------------- saved destinations ---------------- */

	s.scenario(t, "destinations.crud", func(t *testing.T) {
		id, body := s.createDestination(t, map[string]any{"keyPrefix": "crud/"})

		list := s.api.get(t, "/destinations")
		if list.Status != 200 {
			t.Fatalf("GET /destinations = %d", list.Status)
		}
		destinations, _ := list.Map(t)["destinations"].([]any)
		var row map[string]any
		for _, entry := range destinations {
			candidate, _ := entry.(map[string]any)
			if candidate["id"] == id {
				row = candidate
			}
		}
		if row == nil {
			t.Fatal("the new destination is not in the list")
		}
		accessKeyID := body["accessKeyId"].(string)
		if row["accessKeyIdLast4"] != accessKeyID[len(accessKeyID)-4:] {
			t.Fatalf("accessKeyIdLast4 = %v", row["accessKeyIdLast4"])
		}
		if strings.Contains(list.Text(), body["secretAccessKey"].(string)) {
			t.Fatal("the secret access key must never be echoed")
		}
		for _, field := range []string{"id", "name", "provider", "bucket", "region", "endpoint", "forcePathStyle",
			"keyPrefix", "accessKeyIdLast4", "isDefault", "deleteAfterDelivery", "lastTest", "createdAt", "updatedAt"} {
			if _, present := row[field]; !present {
				t.Fatalf("destination is missing %s", field)
			}
		}

		patched := s.api.patch(t, "/destinations/"+id, map[string]any{"name": "renamed by conformance"})
		if patched.Status != 200 {
			t.Fatalf("PATCH = %d %s", patched.Status, patched.Text())
		}
		updated, _ := patched.Map(t)["destination"].(map[string]any)
		if updated["name"] != "renamed by conformance" {
			t.Fatalf("name = %v", updated["name"])
		}

		probe := s.api.post(t, "/destinations/"+id+"/test", nil)
		if probe.Status != 200 {
			t.Fatalf("POST /test = %d %s — a test is ALWAYS 200", probe.Status, probe.Text())
		}
		result := probe.Map(t)
		if result["ok"] != true {
			t.Fatalf("test against the conformance bucket = %s", probe.Text())
		}
		if _, ok := result["latencyMs"].(float64); !ok {
			t.Fatalf("latencyMs = %v", result["latencyMs"])
		}

		removed := s.api.del(t, "/destinations/"+id)
		if removed.Status != 204 || len(removed.Body) != 0 {
			t.Fatalf("DELETE = %d with %d bytes, want 204 and no body", removed.Status, len(removed.Body))
		}

		gone := s.api.patch(t, "/destinations/"+id, map[string]any{"name": "nope"})
		if gone.Status != 404 {
			t.Fatalf("PATCH after DELETE = %d, want 404", gone.Status)
		}
		if code := gone.ErrorCode(t); code != "not_found" {
			t.Fatalf("code = %q", code)
		}
		foreign := s.api.patch(t, "/destinations/00000000-0000-4000-8000-000000000000", map[string]any{"name": "nope"})
		if foreign.Status != 404 {
			t.Fatalf("a foreign destination id = %d, want 404 (never 403)", foreign.Status)
		}

		// The same CRUD cycle through the SDK's Destinations service.
		yes := true
		created, err := s.sdk.Destinations.Create(context.Background(), StorageDestinationInput{
			Name: "conformance sdk " + s.nonce("dest-sdk"), Provider: ProviderS3Compatible,
			Bucket: s.server.Bucket, Region: "auto", Endpoint: s.server.URL, ForcePathStyle: &yes,
			KeyPrefix: "crud-sdk/", AccessKeyID: "CONFORMANCEKEYID", SecretAccessKey: "conformance-secret-access-key",
		})
		if err != nil {
			t.Fatalf("sdk Create: %v", err)
		}
		if created.AccessKeyIDLast4 != "EYID" {
			t.Fatalf("sdk accessKeyIdLast4 = %q", created.AccessKeyIDLast4)
		}
		all, err := s.sdk.Destinations.List(context.Background())
		if err != nil || len(all) == 0 {
			t.Fatalf("sdk List: %v (%d rows)", err, len(all))
		}
		newName := "renamed by the go sdk"
		renamed, err := s.sdk.Destinations.Update(context.Background(), created.ID, StorageDestinationPatch{Name: &newName})
		if err != nil || renamed.Name != newName {
			t.Fatalf("sdk Update: %v / %+v", err, renamed)
		}
		probed, err := s.sdk.Destinations.Test(context.Background(), created.ID)
		if err != nil || !probed.Ok {
			t.Fatalf("sdk Test: %v / %+v", err, probed)
		}
		if err := s.sdk.Destinations.Delete(context.Background(), created.ID); err != nil {
			t.Fatalf("sdk Delete: %v", err)
		}
		if _, err := s.sdk.Destinations.Test(context.Background(), created.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("sdk Test after Delete = %v, want not_found", err)
		}
	})

	s.scenario(t, "destinations.presign", func(t *testing.T) {
		id, _ := s.createDestination(t, map[string]any{"keyPrefix": "exports/"})
		defer s.api.del(t, "/destinations/"+id)

		mismatched := s.api.post(t, "/destinations/"+id+"/presign", map[string]any{"ext": "png", "contentType": "image/jpeg"})
		if mismatched.Status != 400 {
			t.Fatalf("a mismatched ext/contentType = %d, want 400", mismatched.Status)
		}
		if code := mismatched.ErrorCode(t); code != "invalid_input" {
			t.Fatalf("code = %q", code)
		}

		signed := s.api.post(t, "/destinations/"+id+"/presign", map[string]any{"ext": "png", "contentType": "image/png"})
		if signed.Status != 200 {
			t.Fatalf("presign = %d %s", signed.Status, signed.Text())
		}
		body := signed.Map(t)
		if body["method"] != "PUT" {
			t.Fatalf("method = %v", body["method"])
		}
		if body["bucket"] != s.server.Bucket {
			t.Fatalf("bucket = %v", body["bucket"])
		}
		key, _ := body["key"].(string)
		if !regexp.MustCompile(`^exports/\d{4}/\d{2}/\d{2}/export-\d+-[a-z0-9]{6}\.png$`).MatchString(key) {
			t.Fatalf("key = %q", key)
		}
		headers, _ := body["headers"].(map[string]any)
		if headers["content-type"] != "image/png" {
			t.Fatalf("headers = %v", headers)
		}
		expiresAt, _ := body["expiresAt"].(string)
		when, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil || !when.After(time.Now()) {
			t.Fatalf("expiresAt = %q (%v)", expiresAt, err)
		}

		payload := []byte("conformance export bytes")
		putHeaders := map[string]string{}
		for name, value := range headers {
			putHeaders[name], _ = value.(string)
		}
		put := s.anon.do(t, http.MethodPut, body["url"].(string), payload, putHeaders)
		if put.Status != 200 {
			t.Fatalf("PUT to the signed url = %d %s", put.Status, put.Text())
		}
		stored := s.bucketObject(t, key)
		if stored == nil || stored["bytes"] != float64(len(payload)) {
			t.Fatalf("stored = %v", stored)
		}

		// And through the SDK.
		sdkSigned, err := s.sdk.Destinations.PresignUpload(context.Background(), id, PresignRequest{Ext: ExtPNG, ContentType: "image/png"})
		if err != nil {
			t.Fatalf("sdk PresignUpload: %v", err)
		}
		if sdkSigned.Method != "PUT" || sdkSigned.Bucket != s.server.Bucket || sdkSigned.Headers["content-type"] != "image/png" {
			t.Fatalf("sdk presign = %+v", sdkSigned)
		}
		if _, err := s.sdk.Destinations.PresignUpload(context.Background(), id, PresignRequest{Ext: ExtPNG, ContentType: "image/jpeg"}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("sdk mismatched presign = %v, want invalid_input", err)
		}
	})

	s.scenario(t, "jobs.saved-destination", func(t *testing.T) {
		id, _ := s.createDestination(t, map[string]any{"keyPrefix": "saved/"})
		defer s.api.del(t, "/destinations/"+id)

		foreign := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "saved-foreign"),
			"destination": map[string]any{"type": "saved", "id": "00000000-0000-4000-8000-000000000000"},
		})
		if foreign.Status != 404 {
			t.Fatalf("a destination id on another account = %d, want 404", foreign.Status)
		}
		if code := foreign.ErrorCode(t); code != "not_found" {
			t.Fatalf("code = %q", code)
		}

		job := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "saved-dest"),
			"destination": map[string]any{"type": "saved", "id": id},
		})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		jobID, _ := job.Map(t)["jobId"].(string)

		done := s.poll(t, jobID)
		if done["state"] != "succeeded" {
			t.Fatalf("state = %v: %v", done["state"], done)
		}
		destination, _ := done["destination"].(map[string]any)
		if destination["type"] != "saved" || destination["id"] != id {
			t.Fatalf("destination = %v", destination)
		}
		delivery, _ := done["delivery"].(map[string]any)
		if delivery["status"] != "delivered" || delivery["bucket"] != s.server.Bucket {
			t.Fatalf("delivery = %v", delivery)
		}
		key, _ := delivery["key"].(string)
		if !regexp.MustCompile(`^saved/\d{4}/\d{2}/\d{2}/` + regexp.QuoteMeta(jobID) + `\.png$`).MatchString(key) {
			t.Fatalf("delivery key = %q", key)
		}
		stored := s.bucketObject(t, key)
		if stored == nil {
			t.Fatal("nothing at the delivery key")
		}
		if size, _ := stored["bytes"].(float64); size <= 0 {
			t.Fatalf("object bytes = %v", stored["bytes"])
		}

		// And through the SDK's SavedDestination helper.
		result, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromBytes(s.fixture(t, "small.png", s.nonce("saved-sdk")), "image/png"),
			&RunOptions{Destination: SavedDestination(id), PollInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if result.Destination == nil || result.Destination.Type != DestinationSaved || result.Destination.ID != id {
			t.Fatalf("sdk destination = %+v", result.Destination)
		}
		if result.Delivery == nil || result.Delivery.Status != DeliveryDelivered || result.Delivery.Bucket != s.server.Bucket {
			t.Fatalf("sdk delivery = %+v", result.Delivery)
		}
		if s.bucketObject(t, result.Delivery.Key) == nil {
			t.Fatalf("nothing at the SDK run's delivery key %q", result.Delivery.Key)
		}
	})

	s.scenario(t, "jobs.default-destination-and-opt-out", func(t *testing.T) {
		id, _ := s.createDestination(t, map[string]any{"keyPrefix": "default/", "isDefault": true})
		defer s.api.del(t, "/destinations/"+id)

		inherited := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "default-on"),
		})
		if inherited.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", inherited.Status, inherited.Text())
		}
		done := s.poll(t, inherited.Map(t)["jobId"].(string))
		destination, _ := done["destination"].(map[string]any)
		if destination["type"] != "saved" || destination["id"] != id {
			t.Fatalf("the account default was not applied: %v", destination)
		}
		delivery, _ := done["delivery"].(map[string]any)
		if delivery["status"] != "delivered" {
			t.Fatalf("delivery = %v", delivery)
		}
		if key, _ := delivery["key"].(string); !strings.HasPrefix(key, "default/") {
			t.Fatalf("key = %q, want the default/ prefix", key)
		}

		optedOut := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "default-off"),
			"destination": nil,
		})
		if optedOut.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", optedOut.Status, optedOut.Text())
		}
		if optedOut.Map(t)["destination"] != nil {
			t.Fatalf("destination = %v, want null", optedOut.Map(t)["destination"])
		}
		optedOutDone := s.poll(t, optedOut.Map(t)["jobId"].(string))
		if optedOutDone["destination"] != nil || optedOutDone["delivery"] != nil {
			t.Fatalf("an explicit null must opt out of the account default: %v", optedOutDone)
		}

		// An anonymous caller has no account to read a default from.
		anonJob := s.anon.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "default-anon"),
		})
		if anonJob.Status == 202 && anonJob.Map(t)["destination"] != nil {
			t.Fatalf("an anonymous job picked up a default: %s", anonJob.Text())
		}

		// The SDK: nil Destination inherits, NoDestination opts out.
		inheritedSDK, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromBytes(s.fixture(t, "small.png", s.nonce("default-on-sdk")), "image/png"),
			&RunOptions{PollInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if inheritedSDK.Destination == nil || inheritedSDK.Destination.ID != id {
			t.Fatalf("sdk destination = %+v, want the account default", inheritedSDK.Destination)
		}
		if !inheritedSDK.Downloaded {
			t.Fatal("an account default applied server-side must not suppress the download")
		}

		optedOutSDK, err := s.sdk.Run(context.Background(), OpRemoveBackground,
			FromBytes(s.fixture(t, "small.png", s.nonce("default-off-sdk")), "image/png"),
			&RunOptions{NoDestination: true, PollInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatalf("sdk Run: %v", err)
		}
		if optedOutSDK.Destination != nil || optedOutSDK.Delivery != nil {
			t.Fatalf("sdk NoDestination = %+v / %+v, want both nil", optedOutSDK.Destination, optedOutSDK.Delivery)
		}
	})

	/* ---------------- masks & ownership ---------------- */

	s.scenario(t, "jobs.mask-required", func(t *testing.T) {
		assetID := s.uniqueUpload(t, "mask-missing")

		missing := s.api.post(t, "/jobs", map[string]any{"operation": "magic-eraser", "inputAssetId": assetID})
		if missing.Status != 400 {
			t.Fatalf("status = %d, want 400", missing.Status)
		}
		if code := missing.ErrorCode(t); code != "invalid_input" {
			t.Fatalf("code = %q", code)
		}
		envelope, _ := missing.Map(t)["error"].(map[string]any)
		if message, _ := envelope["message"].(string); !strings.Contains(message, "maskAssetId") {
			t.Fatalf("message = %q, should name maskAssetId", message)
		}

		operations := s.anon.get(t, "/operations").Slice(t)
		for _, entry := range operations {
			op, _ := entry.(map[string]any)
			if op["id"] == "magic-eraser" && op["requiresMask"] != true {
				t.Fatal("GET /operations must report requiresMask for magic-eraser")
			}
		}
		if !OpMagicEraser.RequiresMask() {
			t.Fatal("the SDK's own catalog must agree")
		}

		maskID := s.uniqueUpload(t, "mask-bytes")
		withMask := s.api.post(t, "/jobs", map[string]any{
			"operation": "magic-eraser", "inputAssetId": assetID,
			"params": map[string]any{"maskAssetId": maskID},
		})
		if withMask.Status != 202 {
			t.Fatalf("with a mask = %d %s", withMask.Status, withMask.Text())
		}

		// The SDK uploads the mask itself and sets params.maskAssetId.
		result, err := s.sdk.Run(context.Background(), OpMagicEraser,
			FromBytes(s.fixture(t, "small.png", s.nonce("mask-sdk-photo")), "image/png"),
			&RunOptions{
				Mask:         maskInput(s.fixture(t, "small.png", s.nonce("mask-sdk-mask"))),
				PollInterval: 50 * time.Millisecond,
			})
		if err != nil {
			t.Fatalf("sdk Run with a mask: %v", err)
		}
		if !result.Downloaded {
			t.Fatalf("sdk result = %+v", result)
		}
	})

	s.scenario(t, "jobs.foreign-job-is-404", func(t *testing.T) {
		job := s.api.post(t, "/jobs", map[string]any{
			"operation": "remove-background", "inputAssetId": s.uniqueUpload(t, "ownership"),
		})
		if job.Status != 202 {
			t.Fatalf("POST /jobs = %d %s", job.Status, job.Text())
		}
		jobID, _ := job.Map(t)["jobId"].(string)

		if mine := s.api.get(t, "/jobs/"+jobID); mine.Status != 200 {
			t.Fatalf("the owning key = %d, want 200", mine.Status)
		}

		anonymous := s.anon.get(t, "/jobs/"+jobID)
		if anonymous.Status != 404 {
			t.Fatalf("anonymous = %d, want 404 (never 403)", anonymous.Status)
		}
		if code := anonymous.ErrorCode(t); code != "not_found" {
			t.Fatalf("code = %q", code)
		}

		wrongKey := s.anon.do(t, http.MethodGet, "/jobs/"+jobID, nil,
			map[string]string{"Authorization": "Bearer sk_live_some_other_account"})
		if wrongKey.Status != 404 {
			t.Fatalf("a bogus bearer = %d, want 404", wrongKey.Status)
		}

		neverExisted := s.anon.get(t, "/jobs/00000000-0000-4000-8000-000000000000")
		if neverExisted.Status != 404 {
			t.Fatalf("an id that never existed = %d", neverExisted.Status)
		}
		// Indistinguishable: same status, same code. (The message quotes the id
		// back, which is the caller's own input and reveals nothing — the
		// reference implementation compares the code for the same reason.)
		if neverExisted.Status != anonymous.Status || neverExisted.ErrorCode(t) != anonymous.ErrorCode(t) {
			t.Fatalf("a job id must not be an existence oracle:\n  foreign: %d %s\n  unknown: %d %s",
				anonymous.Status, anonymous.Text(), neverExisted.Status, neverExisted.Text())
		}

		// The SDK surfaces it as ErrNotFound.
		if _, err := s.sdk.GetJob(context.Background(), "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("sdk err = %v, want not_found", err)
		}
	})

	/* ---------------- usage ---------------- */

	s.scenario(t, "usage.query", func(t *testing.T) {
		ctx := context.Background()

		// One paid job and one free one, on bytes nobody has submitted
		// before: a cache hit would be free and would not move credits.
		paid, err := s.sdk.CreateJob(ctx, OpUpscale, FromAsset(s.uniqueUpload(t, "usage-paid")), nil)
		if err != nil {
			t.Fatal(err)
		}
		if state := s.poll(t, paid.JobID)["state"]; state != "succeeded" {
			t.Fatalf("paid job state = %v", state)
		}
		free, err := s.sdk.CreateJob(ctx, OpResizeImage, FromAsset(s.uniqueUpload(t, "usage-free")),
			&CreateJobOptions{Params: ResizeImageParams{Width: 4}})
		if err != nil {
			t.Fatal(err)
		}
		if state := s.poll(t, free.JobID)["state"]; state != "succeeded" {
			t.Fatalf("free job state = %v", state)
		}

		report, err := s.sdk.GetUsage(ctx, &UsageQuery{GroupBy: GroupByOperation})
		if err != nil {
			t.Fatal(err)
		}
		if report.GroupBy != GroupByOperation {
			t.Fatalf("groupBy = %q", report.GroupBy)
		}
		for name, value := range map[string]string{"from": report.Range.From, "to": report.Range.To} {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				t.Fatalf("range.%s = %q is not an ISO timestamp: %v", name, value, err)
			}
		}

		// Every scenario shares one seeded account, so these are lower bounds.
		if report.Totals.Jobs < 2 || report.Totals.Credits < 2 || report.Totals.Free < 1 {
			t.Fatalf("totals = %+v, want jobs>=2 credits>=2 free>=1", report.Totals)
		}
		for name, value := range map[string]int{
			"cacheHits":      report.Totals.CacheHits,
			"failed":         report.Totals.Failed,
			"delivered":      report.Totals.Delivered,
			"deliveryFailed": report.Totals.DeliveryFailed,
			"sessions":       report.Totals.Sessions,
			"activeSessions": report.Totals.ActiveSessions,
		} {
			if value < 0 {
				t.Fatalf("totals.%s = %d, want a non-negative integer", name, value)
			}
		}

		upscale := report.Point(string(OpUpscale))
		resize := report.Point(string(OpResizeImage))
		if upscale == nil || resize == nil {
			t.Fatalf("series has no upscale/resize-image bucket: %+v", report.Series)
		}
		// A per-operation bucket's credits is jobs x cost, so with N upscales
		// the honest assertion is "a multiple of 2, at least 2".
		if upscale.Credits < 2 || upscale.Credits%2 != 0 {
			t.Fatalf("upscale credits = %d, want a multiple of 2 that is at least 2", upscale.Credits)
		}
		if resize.Credits != 0 {
			t.Fatalf("resize-image credits = %d, want 0 (it is free)", resize.Credits)
		}
		for _, point := range report.Series {
			if point.Key == "" || point.Label == "" {
				t.Fatalf("series entry has no key/label: %+v", point)
			}
			if point.Jobs < 0 || point.Sessions < 0 {
				t.Fatalf("series entry has negative counters: %+v", point)
			}
		}

		// The cap gauge — an sk_ caller sees every live key on the account.
		if len(report.Keys) < 2 {
			t.Fatalf("keys = %+v, want at least the seeded secret and publishable key", report.Keys)
		}
		for _, key := range report.Keys {
			if key.ID == "" || key.Name == "" {
				t.Fatalf("key row = %+v", key)
			}
			if key.Kind != KeySecret && key.Kind != KeyPublishable {
				t.Fatalf("key kind = %q", key.Kind)
			}
			if key.UsedToday < 0 {
				t.Fatalf("key %s usedToday = %d", key.ID, key.UsedToday)
			}
		}

		byDay, err := s.sdk.GetUsage(ctx, &UsageQuery{GroupBy: GroupByDay})
		if err != nil {
			t.Fatal(err)
		}
		if byDay.GroupBy != GroupByDay {
			t.Fatalf("groupBy = %q", byDay.GroupBy)
		}
		today := time.Now().UTC().Format("2006-01-02")
		if byDay.Point(today) == nil {
			t.Fatalf("the day series has no bucket for %s", today)
		}

		// No credential is a 401, and a backwards range a 400.
		unauthorized := s.anon.get(t, "/usage")
		if unauthorized.Status != 401 || unauthorized.ErrorCode(t) != "unauthorized" {
			t.Fatalf("anonymous GET /usage = %d %s", unauthorized.Status, unauthorized.Text())
		}
		backwards := s.api.get(t, "/usage?from=2026-09-13&to=2026-09-01")
		if backwards.Status != 400 || backwards.ErrorCode(t) != "invalid_input" {
			t.Fatalf("backwards range = %d %s", backwards.Status, backwards.Text())
		}
		_, err = s.sdk.GetUsage(ctx, &UsageQuery{
			From: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("sdk backwards range err = %v, want invalid_input", err)
		}
	})

	/* ---------------- errors ---------------- */

	s.scenario(t, "errors.envelope", func(t *testing.T) {
		responses := []*httpResult{
			s.api.post(t, "/jobs", map[string]any{}),
			s.api.get(t, "/jobs/00000000-0000-4000-8000-000000000000"),
			s.anon.get(t, "/destinations"),
			s.api.post(t, "/uploads", map[string]any{"mime": "application/pdf", "bytes": 10}),
		}
		wantStatus := []int{400, 404, 401, 400}
		for i, res := range responses {
			if res.Status != wantStatus[i] {
				t.Fatalf("response %d = %d, want %d: %s", i, res.Status, wantStatus[i], res.Text())
			}
			// ErrorCode itself asserts the envelope is exactly
			// { error: { code, message } } with a non-empty message.
			code := res.ErrorCode(t)
			if !IsKnownErrorCode(ErrorCode(code)) {
				t.Fatalf("code %q is outside the closed set %v", code, ErrorCodes)
			}
		}
	})

	s.scenario(t, "uploads.unsupported-mime", func(t *testing.T) {
		res := s.api.post(t, "/uploads", map[string]any{"mime": "application/pdf", "bytes": 1024})
		if res.Status != 400 {
			t.Fatalf("status = %d, want 400", res.Status)
		}
		if code := res.ErrorCode(t); code != "unsupported_mime" {
			t.Fatalf("code = %q", code)
		}

		accepted := map[string]bool{}
		for _, entry := range s.anon.get(t, "/operations").Slice(t) {
			op, _ := entry.(map[string]any)
			for _, mime := range op["accept"].([]any) {
				accepted[mime.(string)] = true
			}
		}
		var mimes []string
		for mime := range accepted {
			mimes = append(mimes, mime)
		}
		sort.Strings(mimes)
		if strings.Join(mimes, ",") != "image/jpeg,image/png,image/webp" {
			t.Fatalf("accepted mimes = %v", mimes)
		}

		// The SDK surfaces the same typed code.
		if _, err := s.sdk.Upload(context.Background(), FromBytes([]byte("%PDF-1.7 not an image"), "application/pdf")); !errors.Is(err, ErrUnsupportedMime) {
			t.Fatalf("sdk err = %v, want unsupported_mime", err)
		}
	})

	/* ---------------- embed ---------------- */

	s.scenario(t, "embed.session", func(t *testing.T) {
		allowed := s.anon.post(t, "/embed/sessions", map[string]any{
			"publishableKey": s.server.PublishableKey, "hostOrigin": "http://localhost:3000",
		})
		if allowed.Status != 200 {
			t.Fatalf("allowed origin = %d %s", allowed.Status, allowed.Text())
		}
		session := allowed.Map(t)
		if token, _ := session["token"].(string); token == "" {
			t.Fatalf("token = %v", session["token"])
		}
		expiresAt, _ := session["expiresAt"].(string)
		when, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil || !when.After(time.Now()) {
			t.Fatalf("expiresAt = %q (%v)", expiresAt, err)
		}

		refused := s.anon.post(t, "/embed/sessions", map[string]any{
			"publishableKey": s.server.PublishableKey, "hostOrigin": "https://not-allowed.example",
		})
		if refused.Status != 403 {
			t.Fatalf("disallowed origin = %d, want 403", refused.Status)
		}
		if code := refused.ErrorCode(t); code != "forbidden" {
			t.Fatalf("code = %q", code)
		}

		unauthenticated := s.anon.post(t, "/embed/tokens", map[string]any{"ttlSeconds": 600})
		if unauthenticated.Status != 401 {
			t.Fatalf("POST /embed/tokens with no key = %d, want 401", unauthenticated.Status)
		}
		if code := unauthenticated.ErrorCode(t); code != "unauthorized" {
			t.Fatalf("code = %q", code)
		}

		minted := s.api.post(t, "/embed/tokens", map[string]any{"ttlSeconds": 600})
		if minted.Status != 200 {
			t.Fatalf("POST /embed/tokens = %d %s", minted.Status, minted.Text())
		}
		embedToken, _ := minted.Map(t)["token"].(string)
		if embedToken == "" {
			t.Fatal("no token")
		}
		asEmbed := &rawClient{base: s.server.URL, token: embedToken, hc: s.api.hc}
		list := asEmbed.get(t, "/destinations")
		if list.Status != 200 {
			t.Fatalf("the embed token should authenticate as the account, got %d %s", list.Status, list.Text())
		}

		// And through the SDK's Embed service.
		sdkSession, err := s.sdk.Embed.CreateSession(context.Background(), s.server.PublishableKey, "http://localhost:3000")
		if err != nil || sdkSession.Token == "" {
			t.Fatalf("sdk CreateSession: %v / %+v", err, sdkSession)
		}
		if _, err := s.sdk.Embed.CreateSession(context.Background(), s.server.PublishableKey, "https://not-allowed.example"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("sdk disallowed origin = %v, want forbidden", err)
		}
		sdkToken, err := s.sdk.Embed.CreateToken(context.Background(), EmbedTokenRequest{TTLSeconds: 600})
		if err != nil || sdkToken.Token == "" {
			t.Fatalf("sdk CreateToken: %v / %+v", err, sdkToken)
		}
		embedClient := NewClient(sdkToken.Token, WithBaseURL(s.server.URL), WithRetry(RetryConfig{}))
		if _, err := embedClient.Destinations.List(context.Background()); err != nil {
			t.Fatalf("an embed token must authenticate as the account: %v", err)
		}
		anonSDK := NewClient("", WithBaseURL(s.server.URL), WithRetry(RetryConfig{}))
		if _, err := anonSDK.Embed.CreateToken(context.Background(), EmbedTokenRequest{TTLSeconds: 600}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("sdk CreateToken with no key = %v, want unauthorized", err)
		}
	})

	/* ---------------- webhooks ---------------- */

	s.scenario(t, "webhooks.signature", func(t *testing.T) {
		vector := s.doc.WebhookVector
		body := []byte(vector.RawBody)

		recomputed := webhooks.Sign(body, vector.Timestamp, vector.Secret)
		if recomputed != vector.Signature {
			t.Fatalf("recomputed = %s, want %s", recomputed, vector.Signature)
		}
		if got := webhooks.SignatureHeaderValue(body, vector.Timestamp, vector.Secret); got != vector.SignatureHeaderValue {
			t.Fatalf("header = %s, want %s", got, vector.SignatureHeaderValue)
		}
		if vector.Secret != s.server.WebhookSecret {
			t.Fatalf("the server's webhookSecret (%s) and the vector's (%s) disagree", s.server.WebhookSecret, vector.Secret)
		}

		if err := webhooks.Verify(body, vector.SignatureHeaderValue, vector.Secret); err != nil {
			t.Fatalf("the correct header must verify: %v", err)
		}

		tampered := vector.SignatureHeaderValue[:len(vector.SignatureHeaderValue)-1] + "b"
		if strings.HasSuffix(vector.SignatureHeaderValue, "b") {
			tampered = vector.SignatureHeaderValue[:len(vector.SignatureHeaderValue)-1] + "a"
		}
		refusals := []struct {
			name   string
			body   []byte
			header string
			secret string
		}{
			{"changed secret", body, vector.SignatureHeaderValue, "wrong secret"},
			{"one hex character changed", body, tampered, vector.Secret},
			{"body plus a space", []byte(vector.RawBody + " "), vector.SignatureHeaderValue, vector.Secret},
			{"not a signature header", body, "not a signature header", vector.Secret},
			{"no t=", body, "v1=" + vector.Signature, vector.Secret},
			{"no v1=", body, fmt.Sprintf("t=%d", vector.Timestamp), vector.Secret},
		}
		for _, tc := range refusals {
			if webhooks.Valid(tc.body, tc.header, tc.secret) {
				t.Fatalf("%s must be refused", tc.name)
			}
			if err := webhooks.Verify(tc.body, tc.header, tc.secret); err == nil {
				t.Fatalf("%s must return an error, never panic or pass", tc.name)
			}
		}

		signedAt := time.Unix(vector.Timestamp, 0)
		if err := webhooks.Verify(body, vector.SignatureHeaderValue, vector.Secret,
			webhooks.WithTolerance(300*time.Second), webhooks.WithNow(signedAt.Add(10*time.Second))); err != nil {
			t.Fatalf("now = t+10 inside a 300s window must pass: %v", err)
		}
		if err := webhooks.Verify(body, vector.SignatureHeaderValue, vector.Secret,
			webhooks.WithTolerance(300*time.Second), webhooks.WithNow(signedAt.Add(3600*time.Second))); err == nil {
			t.Fatal("now = t+3600 outside a 300s window must fail even though the MAC is correct")
		}

		event, err := webhooks.ConstructEvent(body, vector.SignatureHeaderValue, vector.Secret)
		if err != nil {
			t.Fatalf("ConstructEvent: %v", err)
		}
		if event.Type != webhooks.EventJobSucceeded || event.Data.Operation != "remove-background" {
			t.Fatalf("event = %+v", event)
		}
	})

	/* ---------------- meta ---------------- */

	s.scenario(t, "openapi.document", func(t *testing.T) {
		res := s.anon.get(t, "/openapi.json")
		if res.Status != 200 {
			t.Fatalf("status = %d", res.Status)
		}
		if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("content-type = %q", res.Header.Get("Content-Type"))
		}
		spec := res.Map(t)
		if spec["openapi"] != "3.1.0" {
			t.Fatalf("openapi = %v", spec["openapi"])
		}
		info, _ := spec["info"].(map[string]any)
		version, _ := info["version"].(string)
		if !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(version) {
			t.Fatalf("info.version = %q", version)
		}
		paths, _ := spec["paths"].(map[string]any)
		for _, path := range []string{
			"/operations", "/uploads", "/uploads/{assetId}/confirm", "/jobs", "/jobs/{id}",
			"/destinations", "/destinations/{id}", "/destinations/{id}/test",
			"/destinations/{id}/presign", "/embed/sessions", "/embed/tokens",
		} {
			if _, present := paths[path]; !present {
				t.Fatalf("the document is missing %s", path)
			}
		}
		components, _ := spec["components"].(map[string]any)
		securitySchemes, _ := components["securitySchemes"].(map[string]any)
		schemeNames := keysOf(securitySchemes)
		if strings.Join(schemeNames, ",") != "ApiKey,EmbedToken,Session" {
			t.Fatalf("securitySchemes = %v", schemeNames)
		}
		schemas, _ := components["schemas"].(map[string]any)
		resize, _ := schemas["OperationParams.resize-image"].(map[string]any)
		if resize["x-credit-cost"] != float64(0) {
			t.Fatalf("resize-image x-credit-cost = %v", resize["x-credit-cost"])
		}

		committed, err := os.ReadFile(filepath.Join(s.root, "docs", "openapi.json"))
		if err != nil {
			t.Fatalf("read docs/openapi.json: %v", err)
		}
		if !bytes.Equal(res.Body, committed) {
			t.Fatal("the served document is not byte-identical to docs/openapi.json — run npm run openapi:export")
		}
	})

	/* ---------------- coverage ---------------- */

	t.Run("coverage", func(t *testing.T) {
		if s.doc.Version != 1 {
			t.Fatalf("scenarios.json version = %d — this suite was written against version 1", s.doc.Version)
		}
		var missing []string
		for _, entry := range s.doc.Scenarios {
			if !s.implemented[entry.ID] {
				missing = append(missing, entry.ID)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("%d scenario(s) in scenarios.json are not implemented here: %v", len(missing), missing)
		}
		t.Logf("implemented all %d scenarios", len(s.doc.Scenarios))
	})
}

// maskInput is a tiny helper: RunOptions.Mask is a *Input, and a composite
// literal cannot be addressed inline.
func maskInput(data []byte) *Input {
	in := FromBytes(data, "image/png")
	return &in
}
