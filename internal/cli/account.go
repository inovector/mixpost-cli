package cli

import (
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
		{Header: "profile", Path: "profile_url", MaxWidth: 40},
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
		list         listFlags
		providers    []string
		keyword      string
		group        string
		unauthorized bool
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List connected social accounts",
		Long: `List the social accounts connected to the workspace. The list is never
paginated; the filters narrow it and combine with each other.`,
		Example: `  mixpost account list --provider instagram --provider linkedin
  mixpost account list --unauthorized
  mixpost account list --keyword acme -o id`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := queryValues{}
			for _, provider := range splitList(providers) {
				query.Add("providers[]", provider)
			}
			if keyword != "" {
				query.Set("keyword", keyword)
			}
			if group != "" {
				query.Set("group", group)
			}
			if unauthorized {
				query.Set("unauthorized", "1")
			}

			return list.run(cmd, env, "accounts", query, accountColumns())
		},
	}
	list.register(cmd)
	f := cmd.Flags()
	f.StringArrayVar(&providers, "provider", nil, "only accounts on this platform, e.g. twitter, linkedin, mastodon; repeatable")
	f.StringVar(&keyword, "keyword", "", "only accounts whose name, username or platform contains this")
	f.StringVar(&group, "group", "", `only accounts in this account group uuid, or "ungrouped"`)
	f.BoolVar(&unauthorized, "unauthorized", false, "only accounts whose connection was lost and need reconnecting")
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
