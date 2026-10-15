package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/inovector/mixpost-cli/internal/config"
	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "auth",
		Short:       "Manage credentials for one or more Mixpost instances",
		Annotations: map[string]string{skipClientAnnotation: "true"},
	}
	cmd.AddCommand(newAuthLoginCommand(), newAuthStatusCommand(), newAuthLogoutCommand())
	return cmd
}

func newAuthLoginCommand() *cobra.Command {
	var (
		urlFlag     string
		token       string
		tokenStdin  bool
		workspace   string
		noVerify    bool
		profileName string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store the Mixpost address and personal access key in a profile",
		Long: `Store credentials in a profile so later commands do not need them.

Without --url the profile uses Mixpost Cloud. For a self-hosted instance, pass
the address you open Mixpost at, including its core path, such as
https://example.com/mixpost. An address without a path is tried with the
default /mixpost core path first and at the site root second.

Create a personal access key from the user menu: click API, then Create a
personal key. It is shown once, so copy it straight away.`,
		Example: `  mixpost auth login --token <personal-access-key>
  mixpost auth login --url https://example.com/mixpost --token <personal-access-key>
  echo "$MIXPOST_TOKEN" | mixpost auth login --url https://example.com/mixpost --token-stdin
  mixpost auth login --url https://staging.example.com/mixpost --token <personal-access-key> --profile staging`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" {
				profileName = env.Profile
			}

			cfg := env.Config
			profile := cfg.EnsureProfile(profileName)

			var candidates []string
			if urlFlag != "" {
				var err error
				if candidates, err = config.BaseURLCandidates(urlFlag); err != nil {
					return usageError(err)
				}
				profile.URL = candidates[0]
			}
			if profile.URL == "" {
				profile.URL = config.DefaultBaseURL
			}

			switch {
			case tokenStdin:
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read the personal access key from stdin: %w", err)
				}
				profile.Token = strings.TrimSpace(string(data))
			case token != "":
				profile.Token = token
			case output.IsTTY(stdin) || profile.Token == "":
				// On a terminal, always ask: logging in again is how a stale
				// key gets replaced. Enter keeps the stored one.
				value, err := promptToken(env.Printer, profile.URL, profile.Token)
				if err != nil {
					return err
				}
				if value != "" {
					profile.Token = value
				}
			}
			if profile.Token == "" {
				return usageError(errors.New("no personal access key provided"))
			}
			if workspace != "" {
				profile.Workspace = workspace
			}

			if cfg.CurrentProfile == "" {
				cfg.CurrentProfile = profileName
			}

			if !noVerify {
				if len(candidates) > 1 {
					profile.URL = pickBaseURL(cmd, profile, candidates)
				}
				if err := verifyProfile(cmd, profile); err != nil {
					return err
				}
			}
			if err := cfg.Save(); err != nil {
				return err
			}

			env.Printer.PrintMessage("Saved profile %q to %s", profileName, cfg.FilePath())
			if profile.Workspace == "" {
				env.Printer.PrintMessage("No workspace set. Run 'mixpost workspace list', then 'mixpost config set workspace <uuid>'.")
			}
			return env.RenderRaw(profileSummary(profileName, profile, true), authColumns())
		},
	}

	f := cmd.Flags()
	f.StringVar(&urlFlag, "url", "", "self-hosted Mixpost address, e.g. https://example.com/mixpost (default: Mixpost Cloud)")
	f.StringVar(&token, "token", "", "personal access key")
	f.BoolVar(&tokenStdin, "token-stdin", false, "read the personal access key from stdin")
	f.StringVar(&workspace, "workspace", "", "default workspace UUID for this profile")
	f.StringVar(&profileName, "profile", "", "profile to write (default: the active profile)")
	f.BoolVar(&noVerify, "no-verify", false, "skip the connectivity check")
	return cmd
}

// pickBaseURL returns the first candidate base URL that serves the Mixpost
// API, judged by whether the workspace listing answers with anything but 404. When none does, the first candidate stays, and verification reports why.
func pickBaseURL(cmd *cobra.Command, profile *config.Profile, candidates []string) string {
	for i, candidate := range candidates {
		probe := *profile
		probe.URL = candidate
		_, err := profileClient(&probe).Workspaces(ctxOf(cmd))
		var apiErr *mixpost.APIError
		if err == nil || (errors.As(err, &apiErr) && apiErr.Status != 404) {
			if i > 0 {
				env.Printer.PrintMessage("No Mixpost API under %s; using %s.", candidates[0], candidate)
			}
			return candidate
		}
	}
	return candidates[0]
}

func profileClient(profile *config.Profile) *mixpost.Client {
	return mixpost.New(mixpost.Options{
		BaseURL:   profile.BaseURL(),
		Token:     profile.Token,
		Workspace: profile.Workspace,
		Timeout:   flags.timeout,
		Retries:   flags.retries,
		Insecure:  flags.insecure,
		UserAgent: userAgent(),
		Debug:     flags.debug,
		DebugOut:  env.Printer.Err,
	})
}

// verifyProfile makes one cheap authenticated call so a typo in the URL or the
// key surfaces at login instead of at the first real command.
func verifyProfile(cmd *cobra.Command, profile *config.Profile) error {
	client := profileClient(profile)
	ctx := ctxOf(cmd)
	if profile.Workspace != "" {
		if _, err := client.Get(ctx, "accounts", nil); err != nil {
			return fmt.Errorf("could not reach the workspace with these credentials: %w", err)
		}
		return nil
	}

	// With no workspace to address, list the workspaces: it needs none and
	// every edition serves it, unlike ping.
	if _, err := client.Workspaces(ctx); err != nil {
		return fmt.Errorf("could not reach Mixpost with these credentials: %w", err)
	}
	return nil
}

// promptToken reads a personal access key from the terminal without echoing it,
// after saying where to create one. current is the stored key, which an empty
// answer keeps.
func promptToken(printer *output.Printer, baseURL, current string) (string, error) {
	if !output.IsTTY(stdin) {
		return "", usageError(fmt.Errorf("pass a personal access key with --token, or pipe it in with --token-stdin; create one at %s", keysPage(baseURL)))
	}
	printer.PrintMessage("Create a personal access key at %s (click Create a personal key), then paste it here.", keysPage(baseURL))
	if current != "" {
		fmt.Fprintf(printer.Err, "Personal access key (Enter keeps %s): ", mixpost.Redact(current))
	} else {
		fmt.Fprint(printer.Err, "Personal access key: ")
	}
	value, err := term.ReadPassword(int(stdin.Fd()))
	fmt.Fprintln(printer.Err)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(value)), nil
}

// keysPage is the dashboard page that lists and creates personal access keys.
// It shares its path with the API, which answers a browser with the page.
func keysPage(baseURL string) string {
	return baseURL + "/api"
}

func newAuthStatusCommand() *cobra.Command {
	var checkAll bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active profile and check that it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := env.Config
			names := []string{env.Profile}
			if checkAll {
				names = cfg.ProfileNames()
			}
			if len(names) == 0 {
				return usageError(errors.New("no profiles configured; run 'mixpost auth login'"))
			}

			rows := make([]map[string]any, 0, len(names))
			failed := false
			for _, name := range names {
				profile := cfg.Profile(name)
				if profile == nil {
					rows = append(rows, map[string]any{
						"profile": name, "authenticated": false, "error": "profile not configured",
					})
					failed = true
					continue
				}
				row := map[string]any{
					"profile":   name,
					"url":       profile.BaseURL(),
					"token":     mixpost.Redact(profile.Token),
					"workspace": profile.Workspace,
					"active":    name == env.Profile,
				}
				if err := verifyProfile(cmd, profile); err != nil {
					row["authenticated"] = false
					row["error"] = err.Error()
					failed = true
				} else {
					row["authenticated"] = true
				}
				rows = append(rows, row)
			}

			raw, err := json.Marshal(map[string]any{"data": rows})
			if err != nil {
				return err
			}
			if err := env.RenderRaw(raw, authColumns()); err != nil {
				return err
			}
			if failed {
				return errors.New("at least one profile could not authenticate")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkAll, "all", false, "check every configured profile")
	return cmd
}

func newAuthLogoutCommand() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove a profile and its stored personal access key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" {
				profileName = env.Profile
			}
			cfg := env.Config
			if cfg.Profile(profileName) == nil {
				return usageError(fmt.Errorf("profile %q is not configured", profileName))
			}
			if err := env.Confirm(fmt.Sprintf("Remove profile %q and its stored personal access key?", profileName)); err != nil {
				return err
			}

			delete(cfg.Profiles, profileName)
			if cfg.CurrentProfile == profileName {
				cfg.CurrentProfile = ""
				if names := cfg.ProfileNames(); len(names) > 0 {
					cfg.CurrentProfile = names[0]
				}
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			env.Printer.PrintMessage("Removed profile %q.", profileName)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile to remove (default: the active profile)")
	return cmd
}

func profileSummary(name string, profile *config.Profile, authenticated bool) []byte {
	raw, _ := json.Marshal(map[string]any{"data": []map[string]any{{
		"profile":       name,
		"url":           profile.BaseURL(),
		"token":         mixpost.Redact(profile.Token),
		"workspace":     profile.Workspace,
		"active":        true,
		"authenticated": authenticated,
	}}})
	return raw
}

func authColumns() []output.Column {
	return []output.Column{
		{Header: "profile", Path: "profile"},
		{Header: "url", Path: "url", MaxWidth: 50},
		{Header: "workspace", Path: "workspace"},
		{Header: "key", Path: "token"},
		{Header: "authenticated", Path: "authenticated"},
	}
}
