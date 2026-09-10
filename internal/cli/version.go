package cli

import (
	"encoding/json"
	"runtime"
	"runtime/debug"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

// Build metadata, set with -ldflags at release time.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print the CLI version",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipClientAnnotation: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := json.Marshal(map[string]any{
				"version": Version(),
				"commit":  buildCommit(),
				"date":    date,
				"go":      runtime.Version(),
				"os":      runtime.GOOS,
				"arch":    runtime.GOARCH,
			})
			if err != nil {
				return err
			}
			return env.RenderRaw(raw, []output.Column{
				{Header: "version", Path: "version"},
				{Header: "commit", Path: "commit"},
				{Header: "date", Path: "date"},
				{Header: "go", Path: "go"},
				{Header: "platform", Fn: func(row any) string {
					return output.Stringify(output.Get(row, "os")) + "/" + output.Stringify(output.Get(row, "arch"))
				}},
			})
		},
	}
}

// Version reports the release version, falling back to the module version
// recorded by `go install`.
func Version() string {
	if version != "dev" && version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func buildCommit() string {
	if commit != "" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

func userAgent() string {
	return "mixpost-cli/" + Version() + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
