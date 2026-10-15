package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"example.com", "https://example.com/mixpost"},
		{"https://example.com", "https://example.com/mixpost"},
		{"https://example.com/", "https://example.com/mixpost"},
		{"https://example.com/mixpost", "https://example.com/mixpost"},
		{"https://example.com/mixpost/", "https://example.com/mixpost"},
		{"https://example.com/mixpost/api", "https://example.com/mixpost"},
		{"https://example.com/mixpost/api/", "https://example.com/mixpost"},
		{"http://localhost:8000/mp", "http://localhost:8000/mp"},
		{"  https://example.com/social  ", "https://example.com/social"},
		{"cloud.mixpost.app", "https://cloud.mixpost.app/p"},
		{"https://cloud.mixpost.app/p/api", "https://cloud.mixpost.app/p"},
	}
	for _, c := range cases {
		got, err := NormalizeBaseURL(c.in)
		if err != nil {
			t.Errorf("NormalizeBaseURL(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBaseURLCandidatesFallBackToTheSiteRoot(t *testing.T) {
	got, err := BaseURLCandidates("example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://example.com/mixpost", "https://example.com"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("candidates = %v, want %v", got, want)
	}

	for _, in := range []string{"https://example.com/social", "cloud.mixpost.app"} {
		if got, _ := BaseURLCandidates(in); len(got) != 1 {
			t.Errorf("BaseURLCandidates(%q) = %v, want a single candidate", in, got)
		}
	}
}

func TestNormalizeBaseURLRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "ftp://example.com", "https://"} {
		if _, err := NormalizeBaseURL(in); err == nil {
			t.Errorf("NormalizeBaseURL(%q) should have failed", in)
		}
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv(EnvConfig, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	profile := cfg.EnsureProfile("staging")
	profile.URL = "https://staging.example.com/mixpost"
	profile.Token = "secret"
	profile.Workspace = "ws-uuid"
	cfg.CurrentProfile = "staging"

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Windows has no POSIX permission bits: Go synthesises the mode from the
	// read-only attribute, so a writable file always reports 0666 whatever
	// Chmod was asked for. Access there is governed by the ACLs the config
	// directory inherits.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("permissions = %o, want 600: the file holds an access token", perm)
		}
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reloaded.Profile("staging")
	if got == nil {
		t.Fatal("profile staging is missing after reload")
	}
	if got.Token != "secret" || got.Workspace != "ws-uuid" {
		t.Errorf("profile = %+v", got)
	}
	if reloaded.CurrentProfile != "staging" {
		t.Errorf("current profile = %q, want staging", reloaded.CurrentProfile)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "absent.yml"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Profiles) != 0 {
		t.Errorf("profiles = %v, want none", cfg.Profiles)
	}
}

func TestProfileNamePrecedence(t *testing.T) {
	cfg := &File{CurrentProfile: "current", Profiles: map[string]*Profile{}}

	if got := cfg.ProfileName("explicit"); got != "explicit" {
		t.Errorf("explicit name = %q", got)
	}

	t.Setenv(EnvProfile, "from-env")
	if got := cfg.ProfileName(""); got != "from-env" {
		t.Errorf("env name = %q, want from-env", got)
	}

	t.Setenv(EnvProfile, "")
	if got := cfg.ProfileName(""); got != "current" {
		t.Errorf("current name = %q, want current", got)
	}

	empty := &File{Profiles: map[string]*Profile{}}
	if got := empty.ProfileName(""); got != DefaultProfile {
		t.Errorf("fallback name = %q, want %q", got, DefaultProfile)
	}
}
