package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/inovector/mixpost-cli/internal/config"
	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
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
		Short: "Store the URL and access token of a Mixpost instance",
		Long: `Store credentials in a profile so later commands do not need them.

The URL is the instance address including the Mixpost core path, for example
https://example.com/mixpost. Generate a token from Access Tokens in the user
menu of your dashboard.`,
		Example: `  mixpost auth login --url https://example.com/mixpost --token <token>
  echo "$MIXPOST_TOKEN" | mixpost auth login --url https://example.com/mixpost --token-stdin
  mixpost auth login --url https://staging.example.com/mixpost --token <token> --profile staging`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" {
				profileName = env.Profile
			}

			cfg := env.Config
			profile := cfg.EnsureProfile(profileName)

			if urlFlag != "" {
				normalized, err := config.NormalizeBaseURL(urlFlag)
				if err != nil {
					return usageError(err)
				}
				profile.URL = normalized
			}
			if profile.URL == "" {
				return usageError(errors.New("--url is required the first time you log in"))
			}

			switch {
			case tokenStdin:
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read token from stdin: %w", err)
				}
				profile.Token = strings.TrimSpace(string(data))
			case token != "":
				profile.Token = token
			case profile.Token == "":
				value, err := promptToken(env.Printer)
				if err != nil {
					return err
				}
				profile.Token = value
			}
			if profile.Token == "" {
				return usageError(errors.New("no token provided"))
			}
			if workspace != "" {
				profile.Workspace = workspace
			}

			if cfg.CurrentProfile == "" {
				cfg.CurrentProfile = profileName
			}

			if !noVerify {
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
	f.StringVar(&urlFlag, "url", "", "instance URL including the core path, e.g. https://example.com/mixpost")
	f.StringVar(&token, "token", "", "access token")
	f.BoolVar(&tokenStdin, "token-stdin", false, "read the access token from stdin")
	f.StringVar(&workspace, "workspace", "", "default workspace UUID for this profile")
	f.StringVar(&profileName, "profile", "", "profile to write (default: the active profile)")
	f.BoolVar(&noVerify, "no-verify", false, "skip the connectivity check")
	return cmd
}

// verifyProfile makes one cheap authenticated call so a typo in the URL or the
// token surfaces at login instead of at the first real command.
func verifyProfile(cmd *cobra.Command, profile *config.Profile) error {
	client := mixpost.New(mixpost.Options{
		BaseURL:   profile.URL,
		Token:     profile.Token,
		Workspace: profile.Workspace,
		Timeout:   flags.timeout,
		Retries:   flags.retries,
		Insecure:  flags.insecure,
		UserAgent: userAgent(),
		Debug:     flags.debug,
		DebugOut:  env.Printer.Err,
	})

	ctx := ctxOf(cmd)
	if profile.Workspace != "" {
		if _, err := client.Get(ctx, "accounts", nil); err != nil {
			return fmt.Errorf("could not reach the workspace with these credentials: %w", err)
		}
		return nil
	}

	// With no workspace to address, try the endpoints that need none. Not every
	// release ships both, so a 404 means "this instance does not offer that
	// check", while a 401 really is a bad token and must stop the login.
	var missing error
	for _, path := range []string{"ping", "workspaces"} {
		_, err := client.Do(ctx, instanceRequest(http.MethodGet, path))
		if err == nil {
			return nil
		}

		var apiErr *mixpost.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			missing = err
			continue
		}
		return fmt.Errorf("could not reach the instance with these credentials: %w", err)
	}

	env.Printer.PrintMessage("Note: this instance offers no credential check that needs no workspace (%v).", missing)
	env.Printer.PrintMessage("Pass --workspace <uuid> to verify the token against a workspace.")
	return nil
}

func promptToken(printer *output.Printer) (string, error) {
	if !output.IsTTY(stdin) {
		return "", usageError(errors.New("pass --token, or pipe the token in with --token-stdin"))
	}
	fmt.Fprint(printer.Err, "Access token: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(line), nil
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
					"url":       profile.URL,
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
		Short: "Remove a profile and its stored token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" {
				profileName = env.Profile
			}
			cfg := env.Config
			if cfg.Profile(profileName) == nil {
				return usageError(fmt.Errorf("profile %q is not configured", profileName))
			}
			if err := env.Confirm(fmt.Sprintf("Remove profile %q and its stored token?", profileName)); err != nil {
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
		"url":           profile.URL,
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
		{Header: "token", Path: "token"},
		{Header: "authenticated", Path: "authenticated"},
	}
}
