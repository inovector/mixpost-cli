package cli

import (
	"net/http"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func newPingCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check that the instance is reachable and the token is valid",
		Long: `Check the instance and the access token.

The endpoint is authenticated, so a success confirms both that the instance
answers and that the token is good. It needs no workspace.

Available in Mixpost Pro only, and only while API access tokens are enabled;
on Enterprise use 'mixpost workspace list' to check a token instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := env.Client.Do(ctxOf(cmd), instanceRequest(http.MethodGet, "ping"))
			if err != nil {
				return err
			}
			return env.Render(resp, []output.Column{{Header: "status", Path: "status"}})
		},
	}
}
