package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inovector/mixpost-cli/internal/config"
)

// testWorkspace is a real uuid, because the client only looks a workspace up by
// name when the configured value is not one.
const testWorkspace = "3bbd0951-5b04-432b-b2a0-688588b0720e"

// run executes the CLI the way a shell would and returns stdout, stderr and
// the exit code.
func run(t *testing.T, handler http.Handler, args ...string) (string, string, int) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	writeProfile(t, server.URL+"/mixpost", testWorkspace)
	return runWithoutServer(t, args...)
}

// writeProfile lays down a configuration file the way `mixpost auth login`
// would, and clears every environment variable that could override it.
func writeProfile(t *testing.T, baseURL, workspace string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yml")
	document := "current_profile: default\nprofiles:\n  default:\n    url: " + baseURL + "\n    token: test-token\n"
	if workspace != "" {
		document += "    workspace: " + workspace + "\n"
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvProfile, "")
	t.Setenv(config.EnvOutput, "")
	return path
}

func runWithoutServer(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var out, errOut bytes.Buffer
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	stdout, stderr, stdin = &out, &errOut, devNull
	t.Cleanup(func() {
		devNull.Close()
		stdout, stderr, stdin = os.Stdout, os.Stderr, os.Stdin
	})

	root := NewRootCommand()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)

	code := ExitOK
	executed, err := root.ExecuteC()
	if err != nil && !isBrokenPipe(err) {
		err = asUsageError(err, commandPath(executed, root))
		printer := env.printerOrDefault()
		printer.Out, printer.Err = &out, &errOut
		renderError(printer, err)
		code = ExitCode(err)
	}
	return out.String(), errOut.String(), code
}

func jsonHandler(t *testing.T, status int, body string, record func(*http.Request)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if record != nil {
			record(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
}

func TestPostListPrintsJSONWhenNotATerminal(t *testing.T) {
	payload := `{"data":[{"uuid":"aaa","status":"draft","versions":[{"content":[{"body":"hi"}]}]}]}`
	out, _, code := run(t, jsonHandler(t, 200, payload, nil), "post", "list")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, out)
	}
}

func TestPostListIDFormatFeedsXargs(t *testing.T) {
	payload := `{"data":[{"uuid":"aaa"},{"uuid":"bbb"}]}`
	out, _, code := run(t, jsonHandler(t, 200, payload, nil), "post", "list", "-o", "id")

	if code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if out != "aaa\nbbb\n" {
		t.Errorf("stdout = %q", out)
	}
}

func TestPostCreateBuildsBodyFromFlags(t *testing.T) {
	var got map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accounts") {
			w.Write([]byte(`{"data":[{"id":7,"uuid":"acc","name":"Acme","username":"acme","provider":"twitter"}]}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"uuid":"new-post"}`))
	})

	_, stderr, code := run(t, handler,
		"post", "create",
		"--text", "Part one",
		"--text", "Part two",
		"--account", "acme",
		"--at", "2026-09-10 15:00",
		"--timezone", "Europe/Chisinau",
	)
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	if got["date"] != "2026-09-10" || got["time"] != "15:00" {
		t.Errorf("schedule = %v %v, want 2026-09-10 15:00", got["date"], got["time"])
	}
	if got["timezone"] != "Europe/Chisinau" {
		t.Errorf("timezone = %v", got["timezone"])
	}
	if got["schedule"] != true {
		t.Errorf("--at should imply schedule=true, got %v", got["schedule"])
	}
	accounts, _ := got["accounts"].([]any)
	if len(accounts) != 1 || accounts[0] != float64(7) {
		t.Errorf("accounts = %v, want [7] resolved from the username", got["accounts"])
	}

	versions, _ := got["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("versions = %v", got["versions"])
	}
	version := versions[0].(map[string]any)
	if version["account_id"] != float64(0) || version["is_original"] != true {
		t.Errorf("version = %v, want the original version", version)
	}
	content, _ := version["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2: repeated --text builds a thread", len(content))
	}
	if content[0].(map[string]any)["body"] != "Part one" {
		t.Errorf("first block = %v", content[0])
	}
}

func TestPostCreateResolvesOptionsAndMedia(t *testing.T) {
	var got map[string]any
	handler := jsonHandler(t, 200, `{"uuid":"new-post"}`, nil)
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"uuid":"new-post"}`))
	})

	_, stderr, code := run(t, handler,
		"post", "create",
		"--text", "Look",
		"--media", "4,5",
		"--account", "1",
		"--option", "mastodon.sensitive=true",
		"--option", "youtube.title=Launch",
	)
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	version := got["versions"].([]any)[0].(map[string]any)
	block := version["content"].([]any)[0].(map[string]any)
	media, _ := block["media"].([]any)
	if len(media) != 2 || media[0] != float64(4) || media[1] != float64(5) {
		t.Errorf("media = %v, want [4 5]", block["media"])
	}

	options := version["options"].(map[string]any)
	if options["mastodon"].(map[string]any)["sensitive"] != true {
		t.Errorf("mastodon options = %v, want a real boolean", options["mastodon"])
	}
	if options["youtube"].(map[string]any)["title"] != "Launch" {
		t.Errorf("youtube options = %v", options["youtube"])
	}
}

func TestPostCreateWithoutContentIsAUsageError(t *testing.T) {
	_, stderr, code := run(t, jsonHandler(t, 200, `{}`, nil), "post", "create", "--account", "1")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "needs content") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestPostDeleteTrashesByDefault(t *testing.T) {
	var got map[string]any
	var method string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})

	_, stderr, code := run(t, handler, "post", "delete", "aaa")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s", method)
	}
	if got["trash"] != true {
		t.Errorf("trash = %v, want true: deleting must not be permanent unless asked", got["trash"])
	}
}

func TestPermanentDeleteWithoutATerminalNeedsYes(t *testing.T) {
	_, stderr, code := run(t, jsonHandler(t, 200, `{}`, nil), "post", "delete", "aaa", "--permanent")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("stderr = %q, want it to point at --yes", stderr)
	}
}

func TestPermanentDeleteWithYesSendsTrashFalse(t *testing.T) {
	var got map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})

	_, stderr, code := run(t, handler, "post", "delete", "aaa", "--permanent", "--yes")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if got["trash"] != false {
		t.Errorf("trash = %v, want false", got["trash"])
	}
	if got["delete_mode"] != "app_only" {
		t.Errorf("delete_mode = %v", got["delete_mode"])
	}
}

func TestDeleteMultiplePostsUsesTheCollectionEndpoint(t *testing.T) {
	var gotPath string
	var got map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})

	_, stderr, code := run(t, handler, "post", "delete", "aaa", "bbb", "--yes")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if !strings.HasSuffix(gotPath, "/api/"+testWorkspace+"/posts") {
		t.Errorf("path = %q", gotPath)
	}
	posts, _ := got["posts"].([]any)
	if len(posts) != 2 {
		t.Errorf("posts = %v", got["posts"])
	}
}

func TestValidationErrorExitsSixAndPrintsFields(t *testing.T) {
	body := `{"message":"Validation errors","errors":{"versions.0.content":["The content is required."]}}`
	_, stderr, code := run(t, jsonHandler(t, 422, body, nil), "post", "create", "--text", "hi")

	if code != ExitValidation {
		t.Fatalf("exit code = %d, want %d", code, ExitValidation)
	}
	var payload struct {
		Error struct {
			Status int                 `json:"status"`
			Errors map[string][]string `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &payload); err != nil {
		t.Fatalf("stderr was not the structured error: %v\n%s", err, stderr)
	}
	if payload.Error.Status != 422 {
		t.Errorf("status = %d", payload.Error.Status)
	}
	if len(payload.Error.Errors["versions.0.content"]) != 1 {
		t.Errorf("errors = %v", payload.Error.Errors)
	}
}

func TestExitCodesPerStatus(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{401, ExitUnauthorized},
		{403, ExitForbidden},
		{404, ExitNotFound},
		{422, ExitValidation},
		{429, ExitRateLimited},
		{500, ExitServer},
	}
	for _, c := range cases {
		_, _, code := run(t, jsonHandler(t, c.status, `{"message":"nope"}`, nil), "post", "get", "aaa", "--retries", "0")
		if code != c.want {
			t.Errorf("HTTP %d gave exit code %d, want %d", c.status, code, c.want)
		}
	}
}

func TestDryRunSendsNothingAndPrintsTheRequest(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a dry run must not reach the server")
	})

	out, stderr, code := run(t, handler, "tag", "create", "--name", "launch", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	var request struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &request); err != nil {
		t.Fatalf("stdout was not the request: %v\n%s", err, out)
	}
	if request.Method != "POST" || !strings.HasSuffix(request.URL, "/api/"+testWorkspace+"/tags") {
		t.Errorf("request = %s %s", request.Method, request.URL)
	}
	if !strings.Contains(string(request.Body), "launch") {
		t.Errorf("body = %s", request.Body)
	}
}

func TestRawCommandBuildsWorkspaceAndInstancePaths(t *testing.T) {
	var gotPath, gotQuery string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Write([]byte(`{"data":[]}`))
	})

	if _, _, code := run(t, handler, "raw", "GET", "posts", "-q", "page=2"); code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasSuffix(gotPath, "/api/"+testWorkspace+"/posts") || gotQuery != "page=2" {
		t.Errorf("path = %q query = %q", gotPath, gotQuery)
	}

	if _, _, code := run(t, handler, "raw", "GET", "ping"); code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasSuffix(gotPath, "/api/ping") {
		t.Errorf("instance path = %q", gotPath)
	}
}

func TestAccountListFilterKeepsEveryFieldOfTheKeptRecords(t *testing.T) {
	payload := `{"data":[
	  {"id":1,"provider":"twitter","username":"acme","data":{"extra":true}},
	  {"id":2,"provider":"linkedin","username":"acme-li"}
	]}`
	out, _, code := run(t, jsonHandler(t, 200, payload, nil), "account", "list", "--provider", "twitter")
	if code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var doc struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(doc.Data) != 1 || doc.Data[0]["username"] != "acme" {
		t.Fatalf("data = %v", doc.Data)
	}
	if _, ok := doc.Data[0]["data"]; !ok {
		t.Error("filtering dropped a field the CLI does not model")
	}
}

func TestUnknownOutputFormatIsAUsageError(t *testing.T) {
	_, stderr, code := run(t, jsonHandler(t, 200, `{}`, nil), "post", "list", "-o", "xml")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "xml") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestMissingCredentialsIsAUsageError(t *testing.T) {
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvProfile, "")

	_, stderr, code := runWithoutServer(t, "post", "list")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "auth login") {
		t.Errorf("stderr = %q, want it to point at 'auth login'", stderr)
	}
}

func TestVersionRunsWithoutCredentials(t *testing.T) {
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")

	out, _, code := runWithoutServer(t, "version")
	if code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out, "version") {
		t.Errorf("stdout = %q", out)
	}
}

func TestConfigSetNormalizesTheURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvProfile, "")

	if _, stderr, code := runWithoutServer(t, "config", "set", "url", "example.com/mixpost/api"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	profile := cfg.Profile("default")
	if profile == nil || profile.URL != "https://example.com/mixpost" {
		t.Errorf("stored url = %+v", profile)
	}
}

func TestPaginationFollowsEveryPageWithAll(t *testing.T) {
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`{"data":[{"uuid":"ccc"}],"links":{"next":null}}`))
			return
		}
		next := server.URL + "/mixpost/api/" + testWorkspace + "/posts?page=2"
		w.Write([]byte(`{"data":[{"uuid":"aaa"},{"uuid":"bbb"}],"links":{"next":"` + next + `"}}`))
	})
	server = httptest.NewServer(handler)
	defer server.Close()

	writeProfile(t, server.URL+"/mixpost", testWorkspace)

	out, _, code := runWithoutServer(t, "post", "list", "--all", "-o", "id")
	if code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if out != "aaa\nbbb\nccc\n" {
		t.Errorf("stdout = %q", out)
	}
}

func TestUnknownFlagExitsWithTheUsageCode(t *testing.T) {
	_, stderr, code := run(t, jsonHandler(t, 200, `{}`, nil), "post", "list", "--nope")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
}

func TestUnknownCommandExitsWithTheUsageCode(t *testing.T) {
	_, _, code := run(t, jsonHandler(t, 200, `{}`, nil), "nonesuch")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestTimezoneShorthand(t *testing.T) {
	var got map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"uuid":"new"}`))
	})

	_, stderr, code := run(t, handler, "post", "create", "--text", "hi", "--tz", "Europe/Chisinau")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if got["timezone"] != "Europe/Chisinau" {
		t.Errorf("timezone = %v, want --tz to be accepted for --timezone", got["timezone"])
	}
}

func TestWorkspaceListUsesTheInstanceEndpoint(t *testing.T) {
	var gotPath string
	payload := `{"data":[
	  {"id":2,"uuid":"ws-a","name":"Acme","pivot":{"role":"admin","can_approve":true,"joined_at":"2025-01-15 10:30:00"}},
	  {"id":1,"uuid":"ws-b","name":"Personal","pivot":{"role":"member","can_approve":false,"joined_at":"2024-11-02 08:12:44"}}
	]}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(payload))
	})

	out, stderr, code := run(t, handler, "workspace", "list", "-o", "id")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if !strings.HasSuffix(gotPath, "/api/workspaces") {
		t.Errorf("path = %q, want the instance endpoint, not a workspace-scoped one", gotPath)
	}
	if out != "ws-a\nws-b\n" {
		t.Errorf("stdout = %q", out)
	}
}

func TestWorkspaceListWorksWithoutAWorkspaceConfigured(t *testing.T) {
	server := httptest.NewServer(jsonHandler(t, 200, `{"data":[]}`, nil))
	defer server.Close()

	writeProfile(t, server.URL+"/mixpost", "")

	if _, stderr, code := runWithoutServer(t, "workspace", "list"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
}

func TestPing(t *testing.T) {
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"status":"ok"}`))
	})

	out, stderr, code := run(t, handler, "ping")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if !strings.HasSuffix(gotPath, "/api/ping") {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(out, `"ok"`) {
		t.Errorf("stdout = %q", out)
	}
}

// The Enterprise admin API is deliberately not wrapped. This guards against a
// panel command tree reappearing by accident.
func TestEnterprisePanelCommandsAreNotRegistered(t *testing.T) {
	root := NewRootCommand()

	for _, args := range [][]string{
		{"user", "list"},
		{"receipt", "list"},
		{"workspace", "create"},
		{"workspace", "delete"},
		{"workspace", "subscription"},
		{"workspace", "user"},
		{"workspace", "ai-credits"},
	} {
		if cmd, _, err := root.Find(args); err == nil && cmd.Name() == args[len(args)-1] {
			t.Errorf("%v is still registered", args)
		}
	}

	// The workspace surface that remains is the read-only listing.
	if cmd, _, err := root.Find([]string{"workspace", "list"}); err != nil || cmd.Name() != "list" {
		t.Errorf("workspace list should still exist, got %v (%v)", cmd, err)
	}
}

// The workspace is deliberately not readable from the environment: it selects
// what a command operates on, and ambient state there makes the same command
// line mean different things in different shells.
func TestWorkspaceIsNotReadFromTheEnvironment(t *testing.T) {
	server := httptest.NewServer(jsonHandler(t, 200, `{"data":[]}`, nil))
	defer server.Close()

	writeProfile(t, server.URL+"/mixpost", "")
	t.Setenv("MIXPOST_WORKSPACE", testWorkspace)

	_, stderr, code := runWithoutServer(t, "post", "list")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: the environment must not select a workspace", code, ExitUsage)
	}
	if !strings.Contains(stderr, "no workspace selected") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestWorkspaceFlagAcceptsAName(t *testing.T) {
	var hitPaths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPaths = append(hitPaths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/api/workspaces") {
			w.Write([]byte(`{"data":[
			  {"id":2,"uuid":"` + testWorkspace + `","name":"Acme","pivot":{"role":"admin"}},
			  {"id":1,"uuid":"f9288040-0d50-4e40-83d6-10ef6dd31d9d","name":"Personal","pivot":{"role":"member"}}
			]}`))
			return
		}
		w.Write([]byte(`{"data":[]}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	writeProfile(t, server.URL+"/mixpost", "")

	if _, stderr, code := runWithoutServer(t, "post", "list", "-w", "acme"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if len(hitPaths) != 2 {
		t.Fatalf("requests = %v, want the lookup then the call", hitPaths)
	}
	if !strings.HasSuffix(hitPaths[0], "/api/workspaces") {
		t.Errorf("first request = %q, want the workspace lookup", hitPaths[0])
	}
	if want := "/api/" + testWorkspace + "/posts"; !strings.HasSuffix(hitPaths[1], want) {
		t.Errorf("second request = %q, want %q", hitPaths[1], want)
	}
}

func TestWorkspaceUUIDCostsNoLookup(t *testing.T) {
	var hitPaths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPaths = append(hitPaths, r.URL.Path)
		w.Write([]byte(`{"data":[]}`))
	})

	if _, stderr, code := run(t, handler, "post", "list"); code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if len(hitPaths) != 1 {
		t.Errorf("requests = %v, want just the call: a uuid needs no lookup", hitPaths)
	}
}

func TestUnknownWorkspaceNameIsAUsageError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":1,"uuid":"` + testWorkspace + `","name":"Acme","pivot":{}}]}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	writeProfile(t, server.URL+"/mixpost", "")

	_, stderr, code := runWithoutServer(t, "post", "list", "-w", "nonesuch")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "Acme") {
		t.Errorf("stderr = %q, want it to list what is available", stderr)
	}
}

func TestWorkspaceNameIsLookedUpOnlyOncePerProcess(t *testing.T) {
	var lookups, pages int
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/workspaces") {
			lookups++
			w.Write([]byte(`{"data":[{"id":1,"uuid":"` + testWorkspace + `","name":"Acme","pivot":{}}]}`))
			return
		}
		pages++
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`{"data":[{"uuid":"b"}],"links":{"next":null}}`))
			return
		}
		next := server.URL + "/mixpost/api/" + testWorkspace + "/posts?page=2"
		w.Write([]byte(`{"data":[{"uuid":"a"}],"links":{"next":"` + next + `"}}`))
	})

	server = httptest.NewServer(handler)
	defer server.Close()
	writeProfile(t, server.URL+"/mixpost", "acme")

	out, stderr, code := runWithoutServer(t, "post", "list", "--all", "-o", "id")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if out != "a\nb\n" {
		t.Errorf("stdout = %q", out)
	}
	if pages != 2 {
		t.Errorf("pages fetched = %d, want 2", pages)
	}
	if lookups != 1 {
		t.Errorf("lookups = %d, want 1: the name is resolved once, not per request", lookups)
	}
}

// Not every release ships the endpoints that need no workspace. Login must
// still succeed then, while a bad token must still fail.
func TestLoginSurvivesAnInstanceWithoutTheInstanceEndpoints(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not found."}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvProfile, "")

	_, stderr, code := runWithoutServer(t, "auth", "login", "--url", server.URL+"/mixpost", "--token", "tok")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "no credential check") {
		t.Errorf("stderr = %q, want the login to say why it could not verify", stderr)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if profile := cfg.Profile("default"); profile == nil || profile.Token != "tok" {
		t.Errorf("the profile was not saved: %+v", profile)
	}
}

func TestLoginStillFailsOnABadToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Unauthenticated."}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvProfile, "")

	_, _, code := runWithoutServer(t, "auth", "login", "--url", server.URL+"/mixpost", "--token", "bad")
	if code != ExitUnauthorized {
		t.Fatalf("exit code = %d, want %d", code, ExitUnauthorized)
	}
}
