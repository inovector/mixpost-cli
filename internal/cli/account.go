package cli

import (
	"strings"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func accountColumns() []output.Column {
	return []output.Column{
		{Header: "id", Path: "id"},
		{Header: "uuid", Path: "uuid"},
		{Header: "name", Path: "name", MaxWidth: 30},
		{Header: "username", Path: "username", MaxWidth: 30},
		{Header: "provider", Path: "provider"},
		{Header: "authorized", Path: "authorized"},
	}
}

func newAccountCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "account",
		Aliases: []string{"accounts"},
		Short:   "Inspect the social accounts connected to a workspace",
	}
	cmd.AddCommand(newAccountListCommand(), newAccountGetCommand())
	return cmd
}

func newAccountListCommand() *cobra.Command {
	var (
		list     listFlags
		provider string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List connected social accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if provider != "" {
				// The endpoint takes no provider filter, so it is applied here.
				list.filter = func(row map[string]any) bool {
					return strings.EqualFold(output.Stringify(row["provider"]), provider)
				}
			}
			return list.run(cmd, env, "accounts", nil, accountColumns())
		},
	}
	list.register(cmd)
	cmd.Flags().StringVar(&provider, "provider", "", "keep only accounts of this provider, e.g. twitter, linkedin, mastodon")
	return cmd
}

func newAccountGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <account-uuid>",
		Short: "Show one social account",
		Args:  exactArgs(1, "mixpost account get <account-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "accounts/"+args[0], nil)
			if err != nil {
				return err
			}
			return env.Render(resp, accountColumns())
		},
	}
}
