package cli

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func workspaceColumns() []output.Column {
	return []output.Column{
		{Header: "uuid", Path: "uuid"},
		{Header: "name", Path: "name", MaxWidth: 34},
		{Header: "role", Path: "pivot.role"},
		{Header: "can_approve", Path: "pivot.can_approve"},
		{Header: "joined", Path: "pivot.joined_at"},
	}
}

func newWorkspaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"workspaces", "ws"},
		Short:   "List the workspaces your token can reach",
	}
	cmd.AddCommand(newWorkspaceListCommand())
	return cmd
}

func newWorkspaceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every workspace the token's owner belongs to",
		Long: `List the workspaces the token's owner belongs to, most recently updated first.

Start here when you do not know your workspace UUID: take a uuid from this list
and give it to -w, or store it with 'mixpost config set workspace'.

The list is not paginated and takes no filters.`,
		Example: `  mixpost workspace list
  mixpost config set workspace "$(mixpost workspace list -o id | head -1)"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := env.Client.Do(ctxOf(cmd), instanceRequest(http.MethodGet, "workspaces"))
			if err != nil {
				// The endpoint is newer than some releases. A 404 here is the
				// instance not offering it, not a mistyped URL.
				var apiErr *mixpost.APIError
				if errors.As(err, &apiErr) && apiErr.Status == 404 {
					return explained(fmt.Errorf("this instance does not provide GET /api/workspaces; read the uuid from the dashboard URL instead: %w", err))
				}
				return err
			}
			return env.Render(resp, workspaceColumns())
		},
	}
}
