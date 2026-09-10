package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inovector/mixpost-cli/internal/config"
	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

var configKeys = []string{"url", "token", "workspace"}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "config",
		Short:       "Inspect and edit the stored configuration",
		Annotations: map[string]string{skipClientAnnotation: "true"},
	}
	cmd.AddCommand(
		newConfigListCommand(),
		newConfigGetCommand(),
		newConfigSetCommand(),
		newConfigUseCommand(),
		newConfigPathCommand(),
	)
	return cmd
}

func newConfigListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List configured profiles",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := env.Config
			rows := make([]map[string]any, 0, len(cfg.Profiles))
			for _, name := range cfg.ProfileNames() {
				profile := cfg.Profile(name)
				rows = append(rows, map[string]any{
					"profile":   name,
					"url":       profile.URL,
					"workspace": profile.Workspace,
					"token":     mixpost.Redact(profile.Token),
					"active":    name == env.Profile,
				})
			}
			raw, err := json.Marshal(map[string]any{"data": rows})
			if err != nil {
				return err
			}
			return env.RenderRaw(raw, []output.Column{
				{Header: "profile", Path: "profile"},
				{Header: "url", Path: "url", MaxWidth: 50},
				{Header: "workspace", Path: "workspace"},
				{Header: "token", Path: "token"},
				{Header: "active", Path: "active"},
			})
		},
	}
}

func newConfigGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "get <" + strings.Join(configKeys, "|") + ">",
		Short:     "Print one setting of the active profile",
		Args:      exactArgs(1, "mixpost config get workspace"),
		ValidArgs: configKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := env.Config.Profile(env.Profile)
			if profile == nil {
				return usageError(fmt.Errorf("profile %q is not configured", env.Profile))
			}
			switch args[0] {
			case "url":
				fmt.Fprintln(env.Printer.Out, profile.URL)
			case "token":
				fmt.Fprintln(env.Printer.Out, profile.Token)
			case "workspace":
				fmt.Fprintln(env.Printer.Out, profile.Workspace)
			default:
				return usageError(fmt.Errorf("unknown key %q (want one of: %s)", args[0], strings.Join(configKeys, ", ")))
			}
			return nil
		},
	}
}

func newConfigSetCommand() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:       "set <" + strings.Join(configKeys, "|") + "> <value>",
		Short:     "Change one setting of a profile",
		Example:   "  mixpost config set workspace 3bbd0951-5b04-432b-b2a0-688588b0720e",
		Args:      exactArgs(2, "mixpost config set workspace <uuid>"),
		ValidArgs: configKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profileName == "" {
				profileName = env.Profile
			}
			profile := env.Config.EnsureProfile(profileName)

			key, value := args[0], args[1]
			switch key {
			case "url":
				normalized, err := config.NormalizeBaseURL(value)
				if err != nil {
					return usageError(err)
				}
				profile.URL = normalized
				value = normalized
			case "token":
				profile.Token = value
				value = mixpost.Redact(value)
			case "workspace":
				profile.Workspace = value
			default:
				return usageError(fmt.Errorf("unknown key %q (want one of: %s)", key, strings.Join(configKeys, ", ")))
			}

			if env.Config.CurrentProfile == "" {
				env.Config.CurrentProfile = profileName
			}
			if err := env.Config.Save(); err != nil {
				return err
			}
			env.Printer.PrintMessage("Set %s=%s in profile %q.", key, value, profileName)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile to edit (default: the active profile)")
	return cmd
}

func newConfigUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile>",
		Short: "Select the profile later commands use by default",
		Args:  exactArgs(1, "mixpost config use staging"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := env.Config
			if cfg.Profile(args[0]) == nil {
				return usageError(fmt.Errorf("profile %q is not configured (see 'mixpost config list')", args[0]))
			}
			cfg.CurrentProfile = args[0]
			if err := cfg.Save(); err != nil {
				return err
			}
			env.Printer.PrintMessage("Now using profile %q.", args[0])
			return nil
		},
	}
}

func newConfigPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the location of the configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(env.Printer.Out, env.Config.FilePath())
			return nil
		},
	}
}
