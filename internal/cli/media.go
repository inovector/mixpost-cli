package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func mediaColumns() []output.Column {
	return []output.Column{
		{Header: "id", Path: "id"},
		{Header: "uuid", Path: "uuid"},
		{Header: "name", Path: "name", MaxWidth: 34},
		{Header: "type", Path: "type"},
		{Header: "mime", Path: "mime_type"},
		{Header: "created", Path: "created_at"},
	}
}

func newMediaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "media",
		Short:   "Manage the workspace media library",
		Aliases: []string{"file", "files"},
	}
	cmd.AddCommand(
		newMediaListCommand(),
		newMediaGetCommand(),
		newMediaUploadCommand(),
		newMediaStatusCommand(),
		newMediaUpdateCommand(),
		newMediaDeleteCommand(),
	)
	return cmd
}

func newMediaListCommand() *cobra.Command {
	var (
		list      listFlags
		mediaType string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List media files",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mediaType != "" {
				list.filter = func(row map[string]any) bool {
					return strings.EqualFold(output.Stringify(row["type"]), mediaType)
				}
			}
			return list.run(cmd, env, "media", nil, mediaColumns())
		},
	}
	list.register(cmd)
	cmd.Flags().StringVar(&mediaType, "type", "", "keep only media of this type, e.g. image, video")
	return cmd
}

func newMediaGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <media-uuid>",
		Short: "Show one media file",
		Args:  exactArgs(1, "mixpost media get <media-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "media/"+args[0], nil)
			if err != nil {
				return err
			}
			return env.Render(resp, mediaColumns())
		},
	}
}

func newMediaUploadCommand() *cobra.Command {
	var (
		altText      string
		chunked      bool
		threshold    string
		concurrency  int
		wait         bool
		pollInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "upload <file|url>...",
		Short: "Upload local files, or have the instance fetch remote URLs",
		Long: `Upload media.

A local path is uploaded directly; large files switch to the chunked endpoints
automatically. An http or https argument is handed to the instance to download,
which the CLI then waits on.`,
		Example: `  mixpost media upload photo.jpg
  mixpost media upload video.mp4 --alt-text "Product demo"
  mixpost media upload https://example.com/photo.jpg
  mixpost media upload *.png -o id`,
		Args: minArgs(1, "mixpost media upload <file|url>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := parseSize(threshold)
			if err != nil {
				return err
			}
			opts := mixpost.UploadOptions{
				AltText:     altText,
				Chunked:     chunked,
				Threshold:   size,
				Concurrency: concurrency,
				Progress: func(msg string) {
					env.Printer.PrintMessage("%s", msg)
				},
			}

			results := make([]json.RawMessage, 0, len(args))
			for _, arg := range args {
				var raw []byte
				var err error
				if isRemoteURL(arg) {
					raw, err = uploadRemote(cmd, arg, altText, wait, pollInterval)
				} else {
					raw, err = env.Client.UploadFile(ctxOf(cmd), arg, opts)
				}
				if err != nil {
					return fmt.Errorf("upload %s: %w", arg, err)
				}
				results = append(results, raw)
			}

			if len(results) == 1 {
				return env.RenderRaw(results[0], mediaColumns())
			}
			raw, err := marshalCollection(results)
			if err != nil {
				return err
			}
			return env.RenderRaw(raw, mediaColumns())
		},
	}

	f := cmd.Flags()
	f.StringVar(&altText, "alt-text", "", "alt text stored with the media")
	f.BoolVar(&chunked, "chunked", false, "always use the chunked endpoints, whatever the file size")
	f.StringVar(&threshold, "chunk-threshold", "8MiB", "file size above which the chunked endpoints are used")
	f.IntVar(&concurrency, "concurrency", 3, "chunks uploaded in parallel")
	f.BoolVar(&wait, "wait", true, "wait for a remote download to finish")
	f.DurationVar(&pollInterval, "poll-interval", 2*time.Second, "how often to poll a remote download")
	return cmd
}

func uploadRemote(cmd *cobra.Command, fileURL, altText string, wait bool, pollInterval time.Duration) ([]byte, error) {
	started, raw, err := env.Client.RemoteUploadInitiate(ctxOf(cmd), fileURL, altText)
	if err != nil {
		return nil, err
	}
	if env.DryRun {
		return raw, nil
	}

	switch started.Status {
	case "completed":
		if len(started.Media) > 0 {
			return started.Media, nil
		}
		return raw, nil
	case "failed":
		message := started.Error
		if message == "" {
			message = "the instance reported no reason"
		}
		return nil, fmt.Errorf("%w: %s", mixpost.ErrRemoteUploadFailed, message)
	}

	if !wait {
		return raw, nil
	}
	if started.DownloadID == "" {
		return nil, fmt.Errorf("the instance queued the download but returned no download id: %s", string(raw))
	}

	env.Printer.PrintMessage("downloading %s on the instance (id %s)", fileURL, started.DownloadID)
	lastProgress := -1
	return env.Client.WaitRemoteUpload(ctxOf(cmd), started.DownloadID, pollInterval, func(status *mixpost.RemoteUpload) {
		if status.Progress != lastProgress {
			lastProgress = status.Progress
			env.Printer.PrintMessage("  %s %d%%", status.Status, status.Progress)
		}
	})
}

func newMediaStatusCommand() *cobra.Command {
	var (
		wait         bool
		pollInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "status <download-id>",
		Short: "Check a remote download started by 'media upload <url>'",
		Args:  exactArgs(1, "mixpost media status <download-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait {
				raw, err := env.Client.WaitRemoteUpload(ctxOf(cmd), args[0], pollInterval, nil)
				if err != nil {
					return err
				}
				return env.RenderRaw(raw, mediaColumns())
			}
			resp, err := env.Client.Get(ctxOf(cmd), "media/remote/"+args[0]+"/status", nil)
			if err != nil {
				return err
			}
			return env.Render(resp, []output.Column{
				{Header: "status", Path: "status"},
				{Header: "progress", Path: "progress"},
				{Header: "media", Path: "media.uuid"},
				{Header: "error", Path: "error", MaxWidth: 40},
			})
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "poll until the download finishes")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "how often to poll")
	return cmd
}

func newMediaUpdateCommand() *cobra.Command {
	var altText string

	cmd := &cobra.Command{
		Use:   "update <media-uuid>",
		Short: "Change the alt text of a media file",
		Args:  exactArgs(1, "mixpost media update <media-uuid> --alt-text <text>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFlags(cmd, "alt-text"); err != nil {
				return err
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPut, "media/"+args[0], nil, map[string]any{
				"alt_text": altText,
			})
			if err != nil {
				return err
			}
			return env.Render(resp, mediaColumns())
		},
	}
	cmd.Flags().StringVar(&altText, "alt-text", "", "alt text (required)")
	return cmd
}

func newMediaDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <media-id>...",
		Aliases: []string{"rm"},
		Short:   "Delete media files by numeric id",
		Args:    minArgs(1, "mixpost media delete <media-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args, "media-id")
			if err != nil {
				return usageError(fmt.Errorf("media is deleted by numeric id, not uuid: %w", err))
			}
			if err := env.Confirm(pluralPrompt("Delete", len(ids), "media file")); err != nil {
				return err
			}

			// The endpoint is documented with "items" as parameters rather than
			// a body, so both are sent; Laravel reads either.
			query := queryValues{}
			for _, id := range ids {
				query.Add("items[]", itoa(id))
			}
			resp, err := env.Client.Do(ctxOf(cmd), mixpost.Request{
				Method: http.MethodDelete,
				Path:   "media",
				Query:  query,
				Body:   map[string]any{"items": ids},
			})
			if err != nil {
				return err
			}
			return env.Render(resp, nil)
		},
	}
}

func isRemoteURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

// parseSize reads a byte size written as a plain number or with a unit suffix.
func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(strings.ToUpper(value))
	if trimmed == "" {
		return 0, nil
	}

	multipliers := []struct {
		suffix string
		factor int64
	}{
		{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
		{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	}
	for _, m := range multipliers {
		if !strings.HasSuffix(trimmed, m.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, m.suffix))
		parsed, err := parseFloat(number)
		if err != nil {
			return 0, usageError(fmt.Errorf("cannot read %q as a size", value))
		}
		return int64(parsed * float64(m.factor)), nil
	}

	parsed, err := parseFloat(trimmed)
	if err != nil {
		return 0, usageError(fmt.Errorf("cannot read %q as a size; try 8MiB", value))
	}
	return int64(parsed), nil
}
