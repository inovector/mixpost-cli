package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inovector/mixpost-cli/internal/config"
)

// recorded is one request the fake API received.
type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Raw    string
}

// fakeAPI answers by "METHOD /path-after-the-workspace" and records every
// request, so a test can assert on what the CLI sent.
type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	routes   map[string]string
}

func newFakeAPI(routes map[string]string) *fakeAPI {
	return &fakeAPI{routes: routes}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/mixpost/api")
	path = strings.TrimPrefix(path, "/"+testWorkspace)

	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)

	f.mu.Lock()
	f.requests = append(f.requests, recorded{Method: r.Method, Path: path, Query: r.URL.Query(), Body: body, Raw: string(raw)})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if payload, ok := f.routes[r.Method+" "+path]; ok {
		w.Write([]byte(payload))
		return
	}
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"message":"no route for ` + r.Method + " " + path + `"}`))
}

// last returns the last request made with this method and path.
func (f *fakeAPI) last(t *testing.T, method, path string) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].Method == method && f.requests[i].Path == path {
			return f.requests[i]
		}
	}
	t.Fatalf("no %s %s request was made; got %+v", method, path, f.requests)
	return recorded{}
}

func (f *fakeAPI) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

const accountsPayload = `{"data":[
  {"id":7,"uuid":"ed1d7f93-ebca-4217-957c-a25fbb2c1075","name":"Acme","username":"acme","provider":"twitter"},
  {"id":9,"uuid":"7eafa7f6-51d7-4597-a775-702a9b8bd884","name":"Acme Inc","username":"acme-inc","provider":"linkedin"}
]}`

const scheduledPost = `{
  "uuid": "8eb4e98d-0000-4000-8000-000000000001",
  "status": "scheduled",
  "accounts": [{"id": 7}],
  "tags": [],
  "versions": [
    {"account_id": 0, "is_original": true, "synced_with": null, "content": [{"body": "Hello", "media": []}], "options": {}},
    {"account_id": 7, "is_original": false, "synced_with": 0, "content": [{"body": "Hello", "media": []}], "options": {}}
  ],
  "scheduled_at": "2026-09-10 12:00:00"
}`

func TestPostUpdateKeepsTheStoredScheduleInUTC(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/8eb4e98d-0000-4000-8000-000000000001": scheduledPost,
		"PUT /posts/8eb4e98d-0000-4000-8000-000000000001": `{"uuid":"8eb4e98d-0000-4000-8000-000000000001"}`,
	})
	_, stderr, code := run(t, api, "post", "update", "8eb4e98d-0000-4000-8000-000000000001", "--text", "Corrected")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	body := api.last(t, "PUT", "/posts/8eb4e98d-0000-4000-8000-000000000001").Body
	if body["date"] != "2026-09-10" || body["time"] != "12:00" || body["timezone"] != "UTC" {
		t.Errorf("schedule = %v %v %v, want the stored UTC time sent as UTC", body["date"], body["time"], body["timezone"])
	}
	versions := body["versions"].([]any)
	if synced := versions[1].(map[string]any)["synced_with"]; synced != float64(0) {
		t.Errorf("synced_with = %v, want the version to keep following the original", synced)
	}
}

func TestPostUpdateWithANewTimeDropsTheCarriedTimezone(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/8eb4e98d-0000-4000-8000-000000000001": scheduledPost,
		"PUT /posts/8eb4e98d-0000-4000-8000-000000000001": `{}`,
	})

	_, stderr, code := run(t, api, "post", "update", "8eb4e98d-0000-4000-8000-000000000001", "--at", "2026-09-11 09:00")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	body := api.last(t, "PUT", "/posts/8eb4e98d-0000-4000-8000-000000000001").Body
	if body["time"] != "09:00" {
		t.Errorf("time = %v", body["time"])
	}
	if _, present := body["timezone"]; present {
		t.Errorf("timezone = %v: a time typed without --timezone is read in the user's timezone, not UTC", body["timezone"])
	}

	_, stderr, code = run(t, api, "post", "update", "8eb4e98d-0000-4000-8000-000000000001", "--at", "2026-09-11 09:00", "--tz", "Europe/Chisinau")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if tz := api.last(t, "PUT", "/posts/8eb4e98d-0000-4000-8000-000000000001").Body["timezone"]; tz != "Europe/Chisinau" {
		t.Errorf("timezone = %v", tz)
	}
}

func TestPostUpdateRefusesHalfAMove(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/8eb4e98d-0000-4000-8000-000000000001": scheduledPost,
		"PUT /posts/8eb4e98d-0000-4000-8000-000000000001": `{}`,
	})
	for _, args := range [][]string{
		{"--time", "09:00"},
		{"--tz", "Europe/Chisinau"},
	} {
		full := append([]string{"post", "update", "8eb4e98d-0000-4000-8000-000000000001"}, args...)
		if _, _, code := run(t, api, full...); code != ExitUsage {
			t.Errorf("%v: exit code = %d, want %d: the stored UTC half would be read in another timezone", args, code, ExitUsage)
		}
	}
	if n := api.count("PUT", "/posts/8eb4e98d-0000-4000-8000-000000000001"); n != 0 {
		t.Errorf("%d updates were sent", n)
	}
}

func TestPostScheduleSendsTimesAndPerAccountTimes(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /accounts": accountsPayload,
		"POST /posts/schedule/8eb4e98d-0000-4000-8000-000000000001": `{"success":true}`,
	})
	_, stderr, code := run(t, api, "post", "schedule", "8eb4e98d-0000-4000-8000-000000000001",
		"--at", "2026-09-10 15:00", "--tz", "Europe/Chisinau",
		"--account-at", "linkedin=2026-09-10 18:00")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	body := api.last(t, "POST", "/posts/schedule/8eb4e98d-0000-4000-8000-000000000001").Body
	if body["postNow"] != false || body["date"] != "2026-09-10" || body["time"] != "15:00" || body["timezone"] != "Europe/Chisinau" {
		t.Errorf("body = %v", body)
	}
	entries, _ := body["accounts_schedule"].([]any)
	if len(entries) != 1 {
		t.Fatalf("accounts_schedule = %v", body["accounts_schedule"])
	}
	entry := entries[0].(map[string]any)
	if entry["account_id"] != float64(9) || entry["date"] != "2026-09-10" || entry["time"] != "18:00" {
		t.Errorf("entry = %v, want linkedin resolved to account 9 at 18:00", entry)
	}
}

func TestPostScheduleNowCannotTakeATime(t *testing.T) {
	api := newFakeAPI(nil)
	if _, _, code := run(t, api, "post", "schedule", "x", "--now", "--at", "2026-09-10 15:00"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestPostApproveSendsABodyOnlyWhenMoving(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"POST /posts/approve/8eb4e98d-0000-4000-8000-000000000001": `{"success":true}`,
	})

	if _, stderr, code := run(t, api, "post", "approve", "8eb4e98d-0000-4000-8000-000000000001"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if raw := api.last(t, "POST", "/posts/approve/8eb4e98d-0000-4000-8000-000000000001").Raw; strings.TrimSpace(raw) != "" {
		t.Errorf("body = %q, want none so the post keeps its time", raw)
	}

	if _, stderr, code := run(t, api, "post", "approve", "8eb4e98d-0000-4000-8000-000000000001", "--at", "2026-09-11T06:00:00Z"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	body := api.last(t, "POST", "/posts/approve/8eb4e98d-0000-4000-8000-000000000001").Body
	if body["date"] != "2026-09-11" || body["time"] != "06:00" || body["timezone"] != "UTC" {
		t.Errorf("body = %v, want an RFC 3339 time sent in UTC", body)
	}
}

func TestPostListFiltersOnTheServer(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /accounts": accountsPayload,
		"GET /posts":    `{"data":[{"uuid":"a"}],"links":{"next":null}}`,
	})
	_, stderr, code := run(t, api, "post", "list", "--status", "scheduled", "--keyword", "launch", "--account", "acme", "--all")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	query := api.last(t, "GET", "/posts").Query
	if got := query["status"]; len(got) != 1 || got[0] != "scheduled" {
		t.Errorf("status = %v", got)
	}
	if got := query["keyword"]; len(got) != 1 || got[0] != "launch" {
		t.Errorf("keyword = %v", got)
	}
	if got := query["accounts[]"]; len(got) != 1 || got[0] != "7" {
		t.Errorf("accounts[] = %v", got)
	}
	if got := query["limit"]; len(got) != 1 || got[0] != "100" {
		t.Errorf("limit = %v, want pages of 100 while walking every page", got)
	}
	if _, present := query["per_page"]; present {
		t.Error("per_page is ignored by the posts endpoint and should not be sent")
	}
}

func TestPostListRejectsAnUnknownStatus(t *testing.T) {
	if _, _, code := run(t, newFakeAPI(nil), "post", "list", "--status", "live"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestPageSizeUsesTheParameterEachEndpointReads(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /media":    `{"data":[]}`,
		"GET /mentions": `{"data":[]}`,
	})
	if _, stderr, code := run(t, api, "media", "list", "--per-page", "20"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if got := api.last(t, "GET", "/media").Query["limit"]; len(got) != 1 || got[0] != "20" {
		t.Errorf("media limit = %v", got)
	}

	if _, stderr, code := run(t, api, "mention", "list", "--per-page", "20"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if got := api.last(t, "GET", "/mentions").Query["per_page"]; len(got) != 1 || got[0] != "20" {
		t.Errorf("mentions per_page = %v", got)
	}
}

func TestAccountListSendsItsFilters(t *testing.T) {
	api := newFakeAPI(map[string]string{"GET /accounts": `{"data":[]}`})
	_, stderr, code := run(t, api, "account", "list", "--provider", "instagram,linkedin", "--keyword", "acme", "--group", "ungrouped", "--unauthorized")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	query := api.last(t, "GET", "/accounts").Query
	if got := query["providers[]"]; len(got) != 2 || got[0] != "instagram" || got[1] != "linkedin" {
		t.Errorf("providers[] = %v", got)
	}
	if query.Get("keyword") != "acme" || query.Get("group") != "ungrouped" || query.Get("unauthorized") != "1" {
		t.Errorf("query = %v", query)
	}
}

const foldersPayload = `{"data":[
  {"uuid":"7c9e6679-7425-40de-944b-e07fc1f90ae7","name":"Brand","parent":null,"media_count":12},
  {"uuid":"1f2e3d4c-5b6a-4978-8899-aabbccddeeff","name":"Q4","parent":"7c9e6679-7425-40de-944b-e07fc1f90ae7","media_count":4},
  {"uuid":"3b3e3d4c-5b6a-4978-8899-aabbccddeeff","name":"Social","parent":null,"media_count":1},
  {"uuid":"2a2e3d4c-5b6a-4978-8899-aabbccddeeff","name":"Q4","parent":"3b3e3d4c-5b6a-4978-8899-aabbccddeeff","media_count":1}
]}`

func TestMediaListResolvesAFolderPath(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /media/folders": foldersPayload,
		"GET /media":         `{"data":[]}`,
	})
	_, stderr, code := run(t, api, "media", "list", "--folder", "Brand/Q4", "--usage", "unused", "--sort", "name")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	query := api.last(t, "GET", "/media").Query
	if query.Get("folder") != "1f2e3d4c-5b6a-4978-8899-aabbccddeeff" || query.Get("usage") != "unused" || query.Get("sort") != "name" {
		t.Errorf("query = %v", query)
	}

	// Two folders are called Q4 and neither sits at the root, so the bare
	// name is ambiguous.
	if _, stderr, code := run(t, api, "media", "list", "--folder", "Q4"); code != ExitUsage || !strings.Contains(stderr, "Brand/Q4") {
		t.Errorf("exit code = %d, stderr = %s; want an ambiguity error naming the paths", code, stderr)
	}
}

func TestMediaUpdateMovesToTheRoot(t *testing.T) {
	api := newFakeAPI(map[string]string{"PUT /media/511b8c2d-c424-3a6a-a154-be271418ff01": `{"success":true}`})
	_, stderr, code := run(t, api, "media", "update", "511b8c2d-c424-3a6a-a154-be271418ff01", "--folder", "root", "--name", "logo.png")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	body := api.last(t, "PUT", "/media/511b8c2d-c424-3a6a-a154-be271418ff01").Body
	if folder, present := body["folder"]; !present || folder != nil {
		t.Errorf("folder = %v (present %v), want null", folder, present)
	}
	if body["name"] != "logo.png" {
		t.Errorf("name = %v", body["name"])
	}
	if _, present := body["alt_text"]; present {
		t.Error("alt_text was not asked for and should not be sent")
	}
}

func TestMediaDeleteAcceptsUUIDsAndBatches(t *testing.T) {
	api := newFakeAPI(map[string]string{"DELETE /media": `{"success":true}`})

	args := []string{"media", "delete", "--yes", "511b8c2d-c424-3a6a-a154-be271418ff01"}
	for i := 1; i <= 500; i++ {
		args = append(args, itoa(i))
	}
	if _, stderr, code := run(t, api, args...); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if n := api.count("DELETE", "/media"); n != 2 {
		t.Fatalf("%d requests, want 2 batches for 501 files", n)
	}
	items := api.last(t, "DELETE", "/media").Body["items"].([]any)
	if len(items) != 1 {
		t.Errorf("last batch = %d items, want 1", len(items))
	}

	if _, _, code := run(t, api, "media", "delete", "--yes", "logo.png"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d for a name", code, ExitUsage)
	}
}

func TestMediaUploadFilesIntoAFolder(t *testing.T) {
	var form map[string][]string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media/folders"):
			w.Write([]byte(foldersPayload))
		case strings.HasSuffix(r.URL.Path, "/media"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse form: %v", err)
			}
			form = r.MultipartForm.Value
			w.Write([]byte(`{"id":"1","uuid":"511b8c2d-c424-3a6a-a154-be271418ff01"}`))
		default:
			w.WriteHeader(404)
		}
	})

	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := run(t, handler, "media", "upload", path, "--folder", "Brand"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if got := form["folder"]; len(got) != 1 || got[0] != "7c9e6679-7425-40de-944b-e07fc1f90ae7" {
		t.Errorf("folder field = %v", got)
	}
}

func TestMediaFolderCreateUnderAParent(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /media/folders":  foldersPayload,
		"POST /media/folders": `{"uuid":"new","name":"Q1","parent":"7c9e6679-7425-40de-944b-e07fc1f90ae7","media_count":0}`,
	})
	if _, stderr, code := run(t, api, "media", "folder", "create", "Q1", "--parent", "Brand"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	body := api.last(t, "POST", "/media/folders").Body
	if body["name"] != "Q1" || body["parent"] != "7c9e6679-7425-40de-944b-e07fc1f90ae7" {
		t.Errorf("body = %v", body)
	}
}

const mentionPayload = `{
  "id": "8f7c1e2b-5d43-4a91-b7e8-2c9d4f6a1b3e",
  "reference": 42,
  "name": "Ana Popescu",
  "avatar": null,
  "identities": [
    {"provider": "bluesky", "handle": "ana.bsky.social", "provider_id": "did:plc:abc", "label": null},
    {"provider": "twitter", "handle": "anapopescu", "provider_id": "123", "label": null}
  ]
}`

func TestMentionCreateBuildsIdentities(t *testing.T) {
	api := newFakeAPI(map[string]string{"POST /mentions": mentionPayload})
	_, stderr, code := run(t, api, "mention", "create", "--name", "Ana Popescu",
		"--identity", "bluesky=@ana.bsky.social", "--provider-id", "bluesky=did:plc:abc:def")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	body := api.last(t, "POST", "/mentions").Body
	identities := body["identities"].([]any)
	if len(identities) != 1 {
		t.Fatalf("identities = %v", identities)
	}
	identity := identities[0].(map[string]any)
	if identity["provider"] != "bluesky" || identity["handle"] != "ana.bsky.social" || identity["provider_id"] != "did:plc:abc:def" {
		t.Errorf("identity = %v, want the @ stripped and the DID kept whole", identity)
	}

	if _, _, code := run(t, api, "mention", "create", "--name", "X", "--identity", "myspace=x"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d for an unknown network", code, ExitUsage)
	}
}

func TestMentionUpdateAppliesFlagsOnTopOfTheStoredIdentities(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /mentions/8f7c1e2b-5d43-4a91-b7e8-2c9d4f6a1b3e": mentionPayload,
		"PUT /mentions/8f7c1e2b-5d43-4a91-b7e8-2c9d4f6a1b3e": mentionPayload,
	})
	_, stderr, code := run(t, api, "mention", "update", "8f7c1e2b-5d43-4a91-b7e8-2c9d4f6a1b3e",
		"--identity", "threads=ana.popescu", "--remove-identity", "twitter")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	body := api.last(t, "PUT", "/mentions/8f7c1e2b-5d43-4a91-b7e8-2c9d4f6a1b3e").Body
	if body["name"] != "Ana Popescu" {
		t.Errorf("name = %v, want the stored name carried over", body["name"])
	}
	var providers []string
	for _, identity := range body["identities"].([]any) {
		providers = append(providers, identity.(map[string]any)["provider"].(string))
	}
	if strings.Join(providers, ",") != "bluesky,threads" {
		t.Errorf("providers = %v, want bluesky kept, twitter removed and threads added", providers)
	}
}

func TestMentionListShowsTheTextToInsert(t *testing.T) {
	api := newFakeAPI(map[string]string{"GET /mentions": `{"data":[` + mentionPayload + `]}`})
	out, stderr, code := run(t, api, "mention", "list", "-o", "csv")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(out, "{{mention:42}}") {
		t.Errorf("stdout = %q, want the insert token", out)
	}
}

func TestAnalyticsAccountResolvesTheAccountAndPeriod(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /accounts": accountsPayload,
		"GET /analytics/accounts/ed1d7f93-ebca-4217-957c-a25fbb2c1075": `{
		  "account": {"name": "Acme"}, "type": "overview", "tabs": ["overview"],
		  "period": {"start_date": "2026-04-01", "end_date": "2026-04-03"},
		  "initial_sync": {"in_progress": false},
		  "data": {"total_followers": 110, "likes": {"value": 210, "change": -4.1}, "audience": [{"date": "2026-04-01", "value": 100}]}
		}`,
	})
	out, stderr, code := run(t, api, "analytics", "account", "acme", "--from", "2026-04-01", "--to", "2026-04-03", "--type", "overview", "-o", "csv")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	query := api.last(t, "GET", "/analytics/accounts/ed1d7f93-ebca-4217-957c-a25fbb2c1075").Query
	if query.Get("date_from") != "2026-04-01" || query.Get("date_to") != "2026-04-03" || query.Get("type") != "overview" {
		t.Errorf("query = %v", query)
	}
	want := "metric,value,change %\nlikes,210,-4.1\ntotal_followers,110,\n"
	if out != want {
		t.Errorf("csv = %q, want %q: headline figures only, charts left to JSON", out, want)
	}

	if _, _, code := run(t, api, "analytics", "account", "acme", "--from", "2026-04-01"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d for half a range", code, ExitUsage)
	}
}

func TestAnalyticsBestTimesSendsAccountUUIDs(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /accounts": accountsPayload,
		"GET /analytics/best-times": `{"timezone":"Europe/Chisinau","times":[
		  {"account_id":7,"account_uuid":"ed1d7f93-ebca-4217-957c-a25fbb2c1075","date":"2026-09-15","time":"13:00"}
		]}`,
	})
	out, stderr, code := run(t, api, "analytics", "best-times", "--account", "twitter", "--tz", "Europe/Chisinau", "-o", "tsv", "--no-headers")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	query := api.last(t, "GET", "/analytics/best-times").Query
	if got := query["accounts[]"]; len(got) != 1 || got[0] != "ed1d7f93-ebca-4217-957c-a25fbb2c1075" {
		t.Errorf("accounts[] = %v", got)
	}
	if query.Get("timezone") != "Europe/Chisinau" {
		t.Errorf("timezone = %q", query.Get("timezone"))
	}
	if out != "7\ted1d7f93-ebca-4217-957c-a25fbb2c1075\t2026-09-15 13:00\n" {
		t.Errorf("tsv = %q", out)
	}
}

func TestAnalyticsJSONIsTheResponseUntouched(t *testing.T) {
	payload := `{"period":{"start_date":"2026-04-01"},"data":{"total_followers":110,"leaderboard":[]}}`
	api := newFakeAPI(map[string]string{"GET /analytics": payload})
	out, stderr, code := run(t, api, "analytics", "summary", "--period", "last_month")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["period"] == nil {
		t.Errorf("stdout = %s, want the whole response", out)
	}
	if got := api.last(t, "GET", "/analytics").Query.Get("period"); got != "last_month" {
		t.Errorf("period = %q", got)
	}
}

func TestLoginFallsBackToTheSiteRoot(t *testing.T) {
	// This instance serves Mixpost from the site root, with no core path.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/workspaces" || r.URL.Path == "/api/ping" {
			w.Write([]byte(`{"data":[],"status":"ok"}`))
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvKey, "")
	t.Setenv(config.EnvProfile, "")

	_, stderr, code := runWithoutServer(t, "auth", "login", "--url", server.URL, "--personal-key", "t")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Profile("default").URL; got != server.URL {
		t.Errorf("stored url = %q, want the site root %q", got, server.URL)
	}
}

func TestMixpostCloudIsTheDefaultURL(t *testing.T) {
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvKey, "key")
	t.Setenv(config.EnvProfile, "")

	out, stderr, code := runWithoutServer(t, "post", "list", "-w", testWorkspace, "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	want := "https://cloud.mixpost.app/p/api/" + testWorkspace + "/posts"
	if !strings.Contains(out, want) {
		t.Errorf("dry run = %s, want a request to %s", out, want)
	}
}

func TestLoginWithoutAURLStoresMixpostCloud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvKey, "")
	t.Setenv(config.EnvProfile, "")

	_, stderr, code := runWithoutServer(t, "auth", "login", "--personal-key", "key", "--no-verify")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Profile("default").URL; got != config.DefaultBaseURL {
		t.Errorf("stored url = %q, want %q", got, config.DefaultBaseURL)
	}
}

func TestLoginWithoutAKeySaysWhereToCreateOne(t *testing.T) {
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvKey, "")
	t.Setenv(config.EnvProfile, "")

	_, stderr, code := runWithoutServer(t, "auth", "login")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "https://cloud.mixpost.app/p/profile/api") {
		t.Errorf("stderr = %q, want the link to the personal keys page", stderr)
	}
}
