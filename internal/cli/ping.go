package cli

import (
	"net/http"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func newPingCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check that Mixpost is reachable and the personal access key is valid",
		Long: `Check Mixpost and the personal access key.

The endpoint is authenticated, so a success confirms both that the instance
answers and that the key is good. It needs no workspace.

Available in self-hosted Mixpost Pro only; on Enterprise and Mixpost Cloud use
'mixpost workspace list' to check a key instead.`,
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
