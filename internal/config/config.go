// Package config loads and persists the CLI configuration: a set of named
// profiles, each pointing at one Mixpost instance.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment variables recognised by the CLI. Every one of them overrides the
// value stored in the active profile, and is in turn overridden by a flag.
const (
	EnvURL      = "MIXPOST_URL"
	EnvToken    = "MIXPOST_TOKEN"
	EnvProfile  = "MIXPOST_PROFILE"
	EnvConfig   = "MIXPOST_CONFIG"
	EnvOutput   = "MIXPOST_OUTPUT"
	EnvInsecure = "MIXPOST_INSECURE"
)

// There is deliberately no MIXPOST_WORKSPACE. The URL and the token say who
// you are; the workspace says what you operate on, and a resource selector
// carried in ambient environment state makes a command mean different things
// in different shells. A wrong URL or token fails loudly; a wrong but valid
// workspace succeeds against the wrong data. It comes from the profile, which
// is written deliberately and can be inspected, or from an explicit -w.

// DefaultCorePath is appended to a base URL that carries no path of its own.
// It mirrors the default value of MIXPOST_CORE_PATH in a Mixpost install.
const DefaultCorePath = "mixpost"

// CloudHost is Mixpost Cloud, which serves every workspace's API under /p.
const CloudHost = "cloud.mixpost.app"

// CloudPath is the path Mixpost Cloud serves Mixpost under.
const CloudPath = "/p"

// DefaultBaseURL is Mixpost Cloud, used whenever no URL is configured, so a
// Cloud user only ever needs a personal key.
const DefaultBaseURL = "https://" + CloudHost + CloudPath

// DefaultProfile is the profile used when none is selected.
const DefaultProfile = "default"

// Profile holds the credentials for a single Mixpost instance.
type Profile struct {
	URL       string `yaml:"url"`
	Token     string `yaml:"token"`
	Workspace string `yaml:"workspace,omitempty"`
}

// BaseURL is the URL the profile addresses: its own, or Mixpost Cloud when it
// sets none.
func (p *Profile) BaseURL() string {
	if p.URL != "" {
		return p.URL
	}
	return DefaultBaseURL
}

// File is the on-disk configuration document.
type File struct {
	CurrentProfile string              `yaml:"current_profile"`
	Profiles       map[string]*Profile `yaml:"profiles"`

	path string
}

// Path returns the location of the configuration file, honouring MIXPOST_CONFIG
// and XDG_CONFIG_HOME.
func Path() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "mixpost", "config.yml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "mixpost", "config.yml"), nil
}

// Load reads the configuration file. A missing file is not an error: it yields
// an empty configuration that Save can later write.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f := &File{Profiles: map[string]*Profile{}, path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]*Profile{}
	}
	f.path = path
	return f, nil
}

// Save writes the configuration back to disk with 0600 permissions. The write
// goes through a temporary file so an interrupted run cannot truncate an
// existing configuration.
func (f *File) Save() error {
	if f.path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		f.path = p
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".config-*.yml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// FilePath reports where this configuration was loaded from.
func (f *File) FilePath() string { return f.path }

// ProfileName resolves which profile to use: an explicit name, then
// MIXPOST_PROFILE, then the profile marked current, then "default".
func (f *File) ProfileName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv(EnvProfile); env != "" {
		return env
	}
	if f.CurrentProfile != "" {
		return f.CurrentProfile
	}
	return DefaultProfile
}

// Profile returns the named profile, or nil when it does not exist.
func (f *File) Profile(name string) *Profile {
	if f.Profiles == nil {
		return nil
	}
	return f.Profiles[name]
}

// EnsureProfile returns the named profile, creating an empty one if needed.
func (f *File) EnsureProfile(name string) *Profile {
	if f.Profiles == nil {
		f.Profiles = map[string]*Profile{}
	}
	if p, ok := f.Profiles[name]; ok {
		return p
	}
	p := &Profile{}
	f.Profiles[name] = p
	return p
}

// ProfileNames lists the configured profiles in stable order.
func (f *File) ProfileNames() []string {
	names := make([]string, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// NormalizeBaseURL turns whatever the user typed into the instance base URL,
// which is the site URL plus the Mixpost core path and nothing else.
//
//	example.com                  -> https://example.com/mixpost
//	https://example.com/mixpost/ -> https://example.com/mixpost
//	https://example.com/mp/api   -> https://example.com/mp
//	cloud.mixpost.app            -> https://cloud.mixpost.app/p
func NormalizeBaseURL(raw string) (string, error) {
	candidates, err := BaseURLCandidates(raw)
	if err != nil {
		return "", err
	}
	return candidates[0], nil
}

// BaseURLCandidates lists the base URLs an address may mean, most likely
// first. An address without a path usually means the default core path, but an
// install can serve Mixpost from the site root, so the root is the fallback
// for a login to try. Mixpost Cloud has one fixed path.
func BaseURLCandidates(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q: missing host", raw)
	}

	path := strings.TrimSuffix(u.Path, "/")
	path = strings.TrimSuffix(path, "/api")
	path = strings.TrimSuffix(path, "/")

	site := u.Scheme + "://" + u.Host
	switch {
	case path != "":
		return []string{site + path}, nil
	case strings.EqualFold(u.Hostname(), CloudHost):
		return []string{site + CloudPath}, nil
	}
	return []string{site + "/" + DefaultCorePath, site}, nil
}
