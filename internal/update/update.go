// Package update tells a user, at most once a day, that a newer release of the
// CLI is out and how to install it the way they installed this one.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Interval is how long a check stays fresh before GitHub is asked again.
const Interval = 24 * time.Hour

// LatestURL redirects to the tag of the newest release, which avoids the
// rate-limited GitHub API.
const LatestURL = "https://github.com/inovector/mixpost-cli/releases/latest"

// state is what the last check found, kept between runs.
type state struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// Checker finds the latest release, from its cache when that is fresh.
type Checker struct {
	Current   string
	CachePath string
	URL       string
	Client    *http.Client
	Now       func() time.Time
}

// New returns a Checker for the running version that caches in the user's
// cache directory.
func New(current string) (*Checker, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Checker{
		Current:   current,
		CachePath: filepath.Join(dir, "mixpost", "update.json"),
		URL:       LatestURL,
		Client: &http.Client{
			Timeout: 3 * time.Second,
			// The redirect is the answer, so it is not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Now: time.Now,
	}, nil
}

// Newer returns the latest release when it is newer than the running one, or
// "" when it is not, or when the running version is not a release at all.
func (c *Checker) Newer(ctx context.Context) (string, error) {
	current, ok := parse(c.Current)
	if !ok {
		return "", nil
	}

	cached, err := c.load()
	if err != nil || c.Now().Sub(cached.CheckedAt) >= Interval {
		latest, fetchErr := c.fetch(ctx)
		if fetchErr != nil {
			// A failed check still counts, so being offline costs one
			// attempt a day rather than one on every run.
			c.save(state{CheckedAt: c.Now().UTC(), Latest: cached.Latest})
			return "", fetchErr
		}
		cached = state{CheckedAt: c.Now().UTC(), Latest: latest}
		c.save(cached)
	}

	latest, ok := parse(cached.Latest)
	if !ok || !latest.newerThan(current) {
		return "", nil
	}
	return strings.TrimPrefix(cached.Latest, "v"), nil
}

func (c *Checker) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	location := resp.Header.Get("Location")
	tag := location[strings.LastIndex(location, "/")+1:]
	if _, ok := parse(tag); !ok {
		return "", fmt.Errorf("no release tag in the redirect to %q", location)
	}
	return tag, nil
}

func (c *Checker) load() (state, error) {
	var s state
	data, err := os.ReadFile(c.CachePath)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// save is best effort: a cache that cannot be written only means checking
// again next time.
func (c *Checker) save(s state) {
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.CachePath), 0o755); err != nil {
		return
	}
	os.WriteFile(c.CachePath, data, 0o644)
}

// semver is a release version. Pre-releases and development builds do not
// parse, so they are never told to update.
type semver [3]int

func parse(v string) (semver, bool) {
	var out semver
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func (v semver) newerThan(other semver) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] > other[i]
		}
	}
	return false
}

// Command is how to update a binary installed at path: through the package
// manager that installed it, or with the install script.
func Command(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	slashed := filepath.ToSlash(path)

	switch {
	case strings.Contains(slashed, "/Cellar/") || strings.Contains(slashed, "/homebrew/") || strings.Contains(slashed, "/linuxbrew/"):
		return "brew upgrade mixpost"
	case strings.Contains(strings.ToLower(slashed), "/scoop/"):
		return "scoop update mixpost"
	case isGoInstall(slashed):
		return "go install github.com/inovector/mixpost-cli/cmd/mixpost@latest"
	case runtime.GOOS == "windows":
		return "download it from https://github.com/inovector/mixpost-cli/releases/latest"
	}
	return "curl -fsSL https://raw.githubusercontent.com/inovector/mixpost-cli/main/install.sh | sh"
}

func isGoInstall(path string) bool {
	dir := filepath.ToSlash(filepath.Dir(path))
	if gobin := os.Getenv("GOBIN"); gobin != "" && filepath.ToSlash(gobin) == dir {
		return true
	}
	return strings.HasSuffix(dir, "/go/bin")
}

// Disabled reports why no check should run in this environment, or "" when
// one may: never in CI, and never when the user opted out.
func Disabled() string {
	switch {
	case os.Getenv("MIXPOST_NO_UPDATE_NOTIFIER") != "":
		return "MIXPOST_NO_UPDATE_NOTIFIER is set"
	case os.Getenv("CI") != "":
		return "running in CI"
	}
	return ""
}
