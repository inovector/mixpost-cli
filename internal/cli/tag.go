package cli

import (
	"net/http"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func tagColumns() []output.Column {
	return []output.Column{
		{Header: "id", Path: "id"},
		{Header: "uuid", Path: "uuid"},
		{Header: "name", Path: "name", MaxWidth: 40},
		{Header: "color", Path: "hex_color"},
	}
}

func newTagCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tag",
		Aliases: []string{"tags"},
		Short:   "Manage workspace tags",
	}
	cmd.AddCommand(
		newTagListCommand(),
		newTagGetCommand(),
		newTagCreateCommand(),
		newTagUpdateCommand(),
		newTagDeleteCommand(),
	)
	return cmd
}

func newTagListCommand() *cobra.Command {
	var list listFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tags",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return list.run(cmd, env, "tags", nil, tagColumns())
		},
	}
	list.register(cmd)
	return cmd
}

func newTagGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <tag-uuid>",
		Short: "Show one tag",
		Args:  exactArgs(1, "mixpost tag get <tag-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "tags/"+args[0], nil)
			if err != nil {
				return err
			}
			return env.Render(resp, tagColumns())
		},
	}
}

func newTagCreateCommand() *cobra.Command {
	var name, hexColor string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a tag",
		Example: `  mixpost tag create --name launch --color "#60a5fa"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "name"); err != nil {
				return err
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "tags", nil, map[string]any{
				"name": name, "hex_color": hexColor,
			})
			if err != nil {
				return err
			}
			return env.Render(resp, tagColumns())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "tag name (required)")
	cmd.Flags().StringVar(&hexColor, "color", "#60a5fa", "HEX color")
	return cmd
}

func newTagUpdateCommand() *cobra.Command {
	var name, hexColor string

	cmd := &cobra.Command{
		Use:   "update <tag-uuid>",
		Short: "Rename or recolor a tag",
		Args:  exactArgs(1, "mixpost tag update <tag-uuid> --name <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Both fields are required by the API, so an unspecified one is
			// carried over from the tag as it stands.
			if !cmd.Flags().Changed("name") || !cmd.Flags().Changed("color") {
				resp, err := env.Client.Get(ctxOf(cmd), "tags/"+args[0], nil)
				if err != nil {
					return err
				}
				var current struct {
					Name     string `json:"name"`
					HexColor string `json:"hex_color"`
				}
				if err := resp.DecodeInto(&current); err != nil {
					return err
				}
				if !cmd.Flags().Changed("name") {
					name = current.Name
				}
				if !cmd.Flags().Changed("color") {
					hexColor = current.HexColor
				}
			}

			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPut, "tags/"+args[0], nil, map[string]any{
				"name": name, "hex_color": hexColor,
			})
			if err != nil {
				return err
			}
			return env.Render(resp, tagColumns())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new tag name")
	cmd.Flags().StringVar(&hexColor, "color", "", "new HEX color")
	return cmd
}

func newTagDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <tag-uuid>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more tags",
		Args:    minArgs(1, "mixpost tag delete <tag-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Confirm(pluralPrompt("Delete", len(args), "tag")); err != nil {
				return err
			}
			for _, uuid := range args {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodDelete, "tags/"+uuid, nil, nil)
				if err != nil {
					return err
				}
				if err := env.Render(resp, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func pluralPrompt(verb string, n int, noun string) string {
	if n == 1 {
		return verb + " 1 " + noun + "?"
	}
	return verb + " " + itoa(n) + " " + noun + "s?"
}

func itoa(n int) string {
	return output.Stringify(float64(n))
}
