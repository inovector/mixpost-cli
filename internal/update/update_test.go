package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// releases answers like GitHub's latest-release URL: a redirect to the tag.
func releases(t *testing.T, tag string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "https://github.com/inovector/mixpost-cli/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func checker(t *testing.T, current, url string, now *time.Time) *Checker {
	t.Helper()
	c, err := New(current)
	if err != nil {
		t.Fatal(err)
	}
	c.CachePath = filepath.Join(t.TempDir(), "update.json")
	c.URL = url
	c.Now = func() time.Time { return *now }
	return c
}

func TestNewerReportsALaterRelease(t *testing.T) {
	var hits atomic.Int32
	server := releases(t, "v0.2.0", &hits)
	now := time.Now()

	latest, err := checker(t, "0.1.0", server.URL, &now).Newer(context.Background())
	if err != nil || latest != "0.2.0" {
		t.Errorf("Newer = %q, %v; want 0.2.0", latest, err)
	}
}

func TestNewerIsQuietWhenUpToDate(t *testing.T) {
	var hits atomic.Int32
	server := releases(t, "v0.1.0", &hits)
	now := time.Now()

	for _, current := range []string{"0.1.0", "v0.1.0", "0.2.0"} {
		if latest, _ := checker(t, current, server.URL, &now).Newer(context.Background()); latest != "" {
			t.Errorf("Newer(%s) = %q, want nothing", current, latest)
		}
	}
}

func TestNewerSkipsDevelopmentBuilds(t *testing.T) {
	var hits atomic.Int32
	server := releases(t, "v9.9.9", &hits)
	now := time.Now()

	for _, current := range []string{"dev", "0.1.0-next", "0.0.1-dev", ""} {
		if latest, _ := checker(t, current, server.URL, &now).Newer(context.Background()); latest != "" {
			t.Errorf("Newer(%q) = %q, want nothing for a build that is not a release", current, latest)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("GitHub was asked %d times for builds that are never told to update", hits.Load())
	}
}

func TestNewerAsksGitHubAtMostOnceADay(t *testing.T) {
	var hits atomic.Int32
	server := releases(t, "v0.2.0", &hits)
	now := time.Now()
	c := checker(t, "0.1.0", server.URL, &now)

	for range 3 {
		if latest, _ := c.Newer(context.Background()); latest != "0.2.0" {
			t.Fatalf("Newer = %q", latest)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1: later runs read the cache", hits.Load())
	}

	now = now.Add(Interval + time.Minute)
	c.Newer(context.Background())
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2 once the cache is a day old", hits.Load())
	}
}

func TestAFailedCheckWaitsADay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // nothing listens: the check fails as it would offline

	now := time.Now()
	c := checker(t, "0.1.0", url, &now)
	if _, err := c.Newer(context.Background()); err == nil {
		t.Fatal("expected the check to fail")
	}

	// The next run reads the recorded attempt instead of trying again.
	var hits atomic.Int32
	c.URL = releases(t, "v0.2.0", &hits).URL
	c.Newer(context.Background())
	if hits.Load() != 0 {
		t.Errorf("hits = %d, want 0 within a day of a failed check", hits.Load())
	}
}

func TestCommandMatchesTheInstallMethod(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/Cellar/mixpost/0.1.0/bin/mixpost": "brew upgrade mixpost",
		"/home/linuxbrew/.linuxbrew/bin/mixpost":         "brew upgrade mixpost",
		"/Users/ana/go/bin/mixpost":                      "go install github.com/inovector/mixpost-cli/cmd/mixpost@latest",
		"/usr/local/bin/mixpost":                         "curl -fsSL https://raw.githubusercontent.com/inovector/mixpost-cli/main/install.sh | sh",
		"/Users/ana/scoop/apps/mixpost/current/mixpost":  "scoop update mixpost",
	}
	if runtime.GOOS == "windows" {
		// Windows has no install script; it is pointed at the release page.
		cases["/usr/local/bin/mixpost"] = "download it from https://github.com/inovector/mixpost-cli/releases/latest"
	}
	for path, want := range cases {
		if got := Command(path); got != want {
			t.Errorf("Command(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestDisabledHonoursCIAndTheOptOut(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("MIXPOST_NO_UPDATE_NOTIFIER", "")
	if reason := Disabled(); reason != "" {
		t.Errorf("Disabled = %q, want enabled", reason)
	}

	t.Setenv("CI", "true")
	if Disabled() == "" {
		t.Error("the check should not run in CI")
	}

	t.Setenv("CI", "")
	t.Setenv("MIXPOST_NO_UPDATE_NOTIFIER", "1")
	if Disabled() == "" {
		t.Error("the check should not run after opting out")
	}
}
