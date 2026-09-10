package mixpost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testWorkspace is a real uuid, because a value that is not one is treated as a
// workspace name and looked up.
const testWorkspace = "3bbd0951-5b04-432b-b2a0-688588b0720e"

func workspacePath(suffix string) string { return "/mixpost/api/" + testWorkspace + "/" + suffix }

func apiPath(suffix string) string { return "/api/" + testWorkspace + "/" + suffix }

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return New(Options{
		BaseURL:   server.URL + "/mixpost",
		Token:     "test-token",
		Workspace: testWorkspace,
		Retries:   0,
		Timeout:   5 * time.Second,
	}), server
}

func TestWorkspaceScopedPathAndAuth(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAccept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.Write([]byte(`{"data":[]}`))
	}))

	if _, err := client.Get(context.Background(), "posts", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := workspacePath("posts"); gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "Bearer test-token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if want := "application/json"; gotAccept != want {
		t.Errorf("Accept = %q, want %q", gotAccept, want)
	}
}

func TestInstancePathsSkipTheWorkspace(t *testing.T) {
	var gotPath string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"data":[]}`))
	}))

	// ping and workspaces sit outside any workspace; panel is the Enterprise
	// admin surface, reachable only through `mixpost raw`.
	for path, want := range map[string]string{
		"ping":        "/mixpost/api/ping",
		"workspaces":  "/mixpost/api/workspaces",
		"panel/users": "/mixpost/api/panel/users",
	} {
		if _, err := client.Get(context.Background(), path, nil); err != nil {
			t.Fatalf("Get(%q): %v", path, err)
		}
		if gotPath != want {
			t.Errorf("Get(%q) hit %q, want %q", path, gotPath, want)
		}
	}
}

func TestInstancePathsResolveWithoutAWorkspace(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	client.Workspace = ""

	for _, path := range []string{"ping", "workspaces"} {
		if _, err := client.Get(context.Background(), path, nil); err != nil {
			t.Errorf("Get(%q) with no workspace: %v", path, err)
		}
	}
}

func TestIsInstancePath(t *testing.T) {
	for _, path := range []string{"ping", "workspaces", "panel", "panel/users", "/ping"} {
		if !IsInstancePath(path) {
			t.Errorf("IsInstancePath(%q) = false, want true", path)
		}
	}
	// A workspace-scoped path must never be mistaken for an instance one, and
	// neither must a path that merely starts with the same letters.
	for _, path := range []string{"posts", "accounts", "media", "workspacesfoo", "pinger"} {
		if IsInstancePath(path) {
			t.Errorf("IsInstancePath(%q) = true, want false", path)
		}
	}
}

func TestWorkspaceScopedCallWithoutWorkspaceFails(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request should never have been sent")
	}))
	client.Workspace = ""

	_, err := client.Get(context.Background(), "posts", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no workspace selected") {
		t.Errorf("error = %v, want it to mention the missing workspace", err)
	}
}

func TestValidationErrorIsParsed(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"message":"Validation errors","errors":{"versions.0.content":["The content is required."]}}`))
	}))

	_, err := client.Call(context.Background(), http.MethodPost, "posts", nil, map[string]any{})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Status != 422 {
		t.Errorf("status = %d, want 422", apiErr.Status)
	}
	if lines := apiErr.ValidationLines(); len(lines) != 1 || lines[0] != "versions.0.content: The content is required." {
		t.Errorf("validation lines = %v", lines)
	}
}

func TestPostIsNotRetriedOnServerError(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"boom"}`))
	}))
	client.Retries = 3

	if _, err := client.Call(context.Background(), http.MethodPost, "posts", nil, map[string]any{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a create must not be repeated after a server error", got)
	}
}

func TestGetIsRetriedOnServerError(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	client.Retries = 3

	if _, err := client.Get(context.Background(), "posts", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestRateLimitedPostIsRetried(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"id":1}`))
	}))
	client.Retries = 2

	if _, err := client.Call(context.Background(), http.MethodPost, "posts", nil, map[string]any{}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2: a 429 means the request was not processed", got)
	}
}

func TestPaginateFollowsNextLink(t *testing.T) {
	var server *httptest.Server
	client, server := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			fmt.Fprintf(w, `{"data":[{"id":1},{"id":2}],"links":{"next":"%s%s?page=2"},"meta":{"total":3}}`, server.URL, workspacePath("posts"))
			return
		}
		w.Write([]byte(`{"data":[{"id":3}],"links":{"next":null},"meta":{"total":3}}`))
	}))

	raw, err := client.Paginate(context.Background(), "posts", nil, 0)
	if err != nil {
		t.Fatalf("Paginate: %v", err)
	}
	var got struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 3 {
		t.Fatalf("items = %d, want 3", len(got.Data))
	}
	if got.Meta["pages"] != float64(2) {
		t.Errorf("meta.pages = %v, want 2", got.Meta["pages"])
	}
}

func TestPaginateStopsAtLimit(t *testing.T) {
	var server *httptest.Server
	client, server := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":1},{"id":2}],"links":{"next":"%s%s?page=9"}}`, server.URL, workspacePath("posts"))
	}))

	raw, err := client.Paginate(context.Background(), "posts", nil, 3)
	if err != nil {
		t.Fatalf("Paginate: %v", err)
	}
	var got struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 3 {
		t.Errorf("items = %d, want 3", len(got.Data))
	}
}

func TestUploadFileSingleRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(path, []byte("image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotField, gotName, gotContent, gotAlt string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("MultipartReader: %v", err)
			return
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			body, _ := io.ReadAll(part)
			switch part.FormName() {
			case "file":
				gotField, gotName, gotContent = "file", part.FileName(), string(body)
			case "alt_text":
				gotAlt = string(body)
			}
		}
		w.Write([]byte(`{"id":"7","uuid":"media-uuid"}`))
	}))

	raw, err := client.UploadFile(context.Background(), path, UploadOptions{AltText: "a photo"})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if gotField != "file" || gotName != "photo.png" || gotContent != "image-bytes" {
		t.Errorf("part = %q/%q/%q", gotField, gotName, gotContent)
	}
	if gotAlt != "a photo" {
		t.Errorf("alt_text = %q, want %q", gotAlt, "a photo")
	}
	if !strings.Contains(string(raw), "media-uuid") {
		t.Errorf("response = %s", raw)
	}
}

func TestUploadFileChunked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	content := strings.Repeat("v", 25)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	chunks := map[string]string{}
	var initiated, completed bool

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media/chunked/initiate"):
			initiated = true
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["mime_type"] != "video/mp4" {
				t.Errorf("mime_type = %v, want video/mp4", body["mime_type"])
			}
			if body["total_size"] != float64(25) {
				t.Errorf("total_size = %v, want 25", body["total_size"])
			}
			w.Write([]byte(`{"upload_uuid":"up-1","chunk_size":10,"total_chunks":3}`))

		case strings.HasSuffix(r.URL.Path, "/upload"):
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("MultipartReader: %v", err)
				return
			}
			var index, data string
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("NextPart: %v", err)
					return
				}
				body, _ := io.ReadAll(part)
				switch part.FormName() {
				case "chunk_index":
					index = string(body)
				case "chunk":
					data = string(body)
				}
			}
			chunks[index] = data
			w.WriteHeader(204)

		case strings.HasSuffix(r.URL.Path, "/complete"):
			completed = true
			w.Write([]byte(`{"id":"9","uuid":"media-uuid","name":"video.mp4"}`))

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))

	raw, err := client.UploadFile(context.Background(), path, UploadOptions{Threshold: 10, Concurrency: 2})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if !initiated || !completed {
		t.Fatalf("initiated = %v, completed = %v", initiated, completed)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if chunks["0"]+chunks["1"]+chunks["2"] != content {
		t.Errorf("reassembled = %q, want %q", chunks["0"]+chunks["1"]+chunks["2"], content)
	}
	if !strings.Contains(string(raw), "media-uuid") {
		t.Errorf("response = %s", raw)
	}
}

func TestChunkedUploadAbortsAfterAFailedChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, []byte(strings.Repeat("v", 25)), 0o600); err != nil {
		t.Fatal(err)
	}

	var aborted atomic.Bool
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media/chunked/initiate"):
			w.Write([]byte(`{"upload_uuid":"up-1","chunk_size":10,"total_chunks":3}`))
		case strings.HasSuffix(r.URL.Path, "/upload"):
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(422)
			w.Write([]byte(`{"message":"chunk rejected"}`))
		case r.Method == http.MethodDelete:
			aborted.Store(true)
			w.WriteHeader(204)
		}
	}))

	if _, err := client.UploadFile(context.Background(), path, UploadOptions{Threshold: 10, Concurrency: 1}); err == nil {
		t.Fatal("expected an error")
	}
	if !aborted.Load() {
		t.Error("the upload session was not aborted")
	}
}

func TestWaitRemoteUploadReturnsMedia(t *testing.T) {
	var calls atomic.Int32
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"status":"downloading","progress":40}`))
			return
		}
		w.Write([]byte(`{"status":"completed","progress":100,"media":{"id":"3","uuid":"media-uuid"}}`))
	}))

	raw, err := client.WaitRemoteUpload(context.Background(), "dl-1", time.Millisecond, nil)
	if err != nil {
		t.Fatalf("WaitRemoteUpload: %v", err)
	}
	if !strings.Contains(string(raw), "media-uuid") {
		t.Errorf("media = %s", raw)
	}
}

func TestWaitRemoteUploadReportsFailure(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"failed","error":"Download timed out"}`))
	}))

	_, err := client.WaitRemoteUpload(context.Background(), "dl-1", time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Download timed out") {
		t.Errorf("error = %v, want the server reason", err)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request should never have been sent")
	}))
	client.DryRun = true

	resp, err := client.Call(context.Background(), http.MethodPost, "posts", nil, map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Status != 0 {
		t.Errorf("status = %d, want 0", resp.Status)
	}
	if !strings.Contains(resp.Request.URL, apiPath("posts")) {
		t.Errorf("url = %q", resp.Request.URL)
	}
	if string(resp.Request.Body) != `{"a":1}` {
		t.Errorf("body = %s", resp.Request.Body)
	}
	if strings.Contains(resp.Request.Headers["Authorization"], "test-token") {
		t.Error("the dry run leaked the full token")
	}
}

func TestDetectMIME(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectMIME(path)
	if err != nil {
		t.Fatalf("DetectMIME: %v", err)
	}
	if got != "video/mp4" {
		t.Errorf("mime = %q, want video/mp4", got)
	}
}

// multipartFieldNames is a guard against a rename breaking the chunk contract.
func TestChunkFieldNames(t *testing.T) {
	factory := fileMultipart(map[string]string{"upload_session": "up-1", "chunk_index": "0"}, "chunk", "f.part0", writeTemp(t, "abc"), 0, 0)
	body, contentType, err := factory()
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	boundary := strings.TrimPrefix(contentType, "multipart/form-data; boundary=")
	reader := multipart.NewReader(body, boundary)

	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		io.Copy(io.Discard, part)
		seen[part.FormName()] = true
	}
	for _, name := range []string{"upload_session", "chunk_index", "chunk"} {
		if !seen[name] {
			t.Errorf("missing part %q", name)
		}
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDryRunUploadDescribesTheFlowWithoutSending(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.png")
	big := filepath.Join(dir, "big.mp4")
	if err := os.WriteFile(small, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat("v", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a dry run must not reach the server")
	}))
	client.DryRun = true

	for _, tc := range []struct{ path, mode string }{{small, "single"}, {big, "chunked"}} {
		raw, err := client.UploadFile(context.Background(), tc.path, UploadOptions{Threshold: 1024})
		if err != nil {
			t.Fatalf("UploadFile(%s): %v", tc.path, err)
		}
		var plan struct {
			DryRun   bool     `json:"dry_run"`
			Mode     string   `json:"mode"`
			Size     int64    `json:"size"`
			Requests []string `json:"requests"`
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatalf("decode plan: %v\n%s", err, raw)
		}
		if !plan.DryRun || plan.Mode != tc.mode {
			t.Errorf("plan for %s = %+v, want mode %q", tc.path, plan, tc.mode)
		}
		if len(plan.Requests) == 0 {
			t.Errorf("plan for %s lists no requests", tc.path)
		}
	}
}

func TestWorkspaceNameResolvedOnceUnderConcurrentChunkUploads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	content := strings.Repeat("v", 60)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var lookups atomic.Int32
	var mu sync.Mutex
	chunks := map[string]string{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/workspaces"):
			lookups.Add(1)
			w.Write([]byte(`{"data":[{"id":1,"uuid":"` + testWorkspace + `","name":"Acme"}]}`))

		case strings.HasSuffix(r.URL.Path, "/media/chunked/initiate"):
			if !strings.Contains(r.URL.Path, testWorkspace) {
				t.Errorf("initiate hit %q, want the resolved uuid", r.URL.Path)
			}
			w.Write([]byte(`{"upload_uuid":"up-1","chunk_size":10,"total_chunks":6}`))

		case strings.HasSuffix(r.URL.Path, "/upload"):
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("MultipartReader: %v", err)
				return
			}
			var index, data string
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("NextPart: %v", err)
					return
				}
				body, _ := io.ReadAll(part)
				switch part.FormName() {
				case "chunk_index":
					index = string(body)
				case "chunk":
					data = string(body)
				}
			}
			mu.Lock()
			chunks[index] = data
			mu.Unlock()
			w.WriteHeader(204)

		case strings.HasSuffix(r.URL.Path, "/complete"):
			w.Write([]byte(`{"id":"9","uuid":"media-uuid"}`))
		}
	}))
	client.Workspace = "Acme"

	if _, err := client.UploadFile(context.Background(), path, UploadOptions{Threshold: 10, Concurrency: 4}); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if got := lookups.Load(); got != 1 {
		t.Errorf("lookups = %d, want 1 even with chunks in flight together", got)
	}
	if len(chunks) != 6 {
		t.Errorf("chunks = %d, want 6", len(chunks))
	}
}

func TestLooksLikeUUID(t *testing.T) {
	if !looksLikeUUID("3bbd0951-5b04-432b-b2a0-688588b0720e") {
		t.Error("a uuid should be recognised")
	}
	for _, value := range []string{"acme", "ws-uuid", "", "3bbd0951-5b04-432b-b2a0-688588b0720", "not-a-uuid-at-all-really-nope-nope"} {
		if looksLikeUUID(value) {
			t.Errorf("looksLikeUUID(%q) = true, want false", value)
		}
	}
}
