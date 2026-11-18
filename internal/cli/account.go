package cli

import (
	"net/http"
	"net/url"

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
	cmd.AddCommand(newAccountListCommand(), newAccountGetCommand(), newAccountActionsCommand(), newAccountRunCommand())
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

func accountActionColumns() []output.Column {
	return []output.Column{
		{Header: "name", Path: "name"},
		{Header: "read only", Path: "read_only"},
		{Header: "description", Path: "description", MaxWidth: 80},
	}
}

func newAccountActionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "actions <account-uuid>",
		Short: "List what only the account's platform does",
		Long: `List the account's actions, such as creating a Pinterest board, with the
input each one takes. Run one with "mixpost account run"; -o json shows the
input of each action.`,
		Example: `  mixpost account actions 85347d5e-9724-4cb6-b91c-82d6d6d4e992
  mixpost account actions 85347d5e-9724-4cb6-b91c-82d6d6d4e992 -o json`,
		Args: exactArgs(1, "mixpost account actions <account-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "accounts/"+url.PathEscape(args[0])+"/actions", nil)
			if err != nil {
				return err
			}
			return env.Render(resp, accountActionColumns())
		},
	}
}

func newAccountRunCommand() *cobra.Command {
	var (
		inputs    []string
		inputFile string
	)

	cmd := &cobra.Command{
		Use:   "run <account-uuid> <action>",
		Short: "Run one of the account's actions on its platform",
		Long: `Run one of the account's actions, as "mixpost account actions" lists them.
Its input is given with --input name=value, repeatable, or as a JSON file.`,
		Example: `  mixpost account run 85347d5e-9724-4cb6-b91c-82d6d6d4e992 list-boards
  mixpost account run 85347d5e-9724-4cb6-b91c-82d6d6d4e992 create-board --input name=Recipes --input privacy=SECRET`,
		Args: exactArgs(2, "mixpost account run <account-uuid> <action>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := buildActionInput(inputFile, inputs)
			if err != nil {
				return err
			}
			path := "accounts/" + url.PathEscape(args[0]) + "/actions/" + url.PathEscape(args[1])
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, path, nil, input)
			if err != nil {
				return err
			}
			return env.Render(resp, nil)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&inputs, "input", nil, "an input field as name=value; repeatable")
	f.StringVar(&inputFile, "input-file", "", "JSON file holding the input object")
	return cmd
}

func buildActionInput(file string, pairs []string) (map[string]any, error) {
	input := map[string]any{}
	if file != "" {
		loaded, err := readJSONObject(file)
		if err != nil {
			return nil, err
		}
		input = loaded
	}
	for _, pair := range pairs {
		path, raw, err := parseKeyValue(pair, "input")
		if err != nil {
			return nil, err
		}
		value, err := inferValue(raw)
		if err != nil {
			return nil, err
		}
		if err := setNested(input, path, value); err != nil {
			return nil, err
		}
	}
	return input, nil
}
