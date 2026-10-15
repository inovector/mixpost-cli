package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
		{Header: "size", Fn: func(row any) string {
			size, ok := toInt(output.Get(row, "size"))
			if !ok {
				return ""
			}
			return mixpost.HumanBytes(int64(size))
		}},
		{Header: "folder", Path: "folder.name", MaxWidth: 20},
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
		newMediaFolderCommand(),
	)
	return cmd
}

var (
	mediaTypes  = []string{"image", "gif", "video"}
	mediaUsages = []string{"published", "scheduled", "draft", "unused"}
	mediaSorts  = []string{"newest", "oldest", "name", "size"}
)

func newMediaListCommand() *cobra.Command {
	var (
		list      listFlags
		mediaType string
		folder    string
		keyword   string
		usage     string
		favorite  bool
		sort      string
		mimeTypes []string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List media files",
		Long: `List the media library, newest first.

Without --folder the whole library is listed flat, across every folder;
--folder root lists the files filed in no folder. --keyword searches the whole
library, so --folder and --favorite are ignored while it is set.`,
		Example: `  mixpost media list --type video --usage unused
  mixpost media list --folder Brand/Q4 --sort name
  mixpost media list --keyword logo -o id`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := queryValues{}
			if mediaType != "" {
				if !contains(mediaTypes, mediaType) {
					return usageError(fmt.Errorf("--type must be one of: %s", strings.Join(mediaTypes, ", ")))
				}
				query.Set("type", mediaType)
			}
			if folder != "" {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, folder)
				if err != nil {
					return err
				}
				if root {
					uuid = "root"
				}
				query.Set("folder", uuid)
			}
			if keyword != "" {
				query.Set("keyword", keyword)
			}
			if usage != "" {
				if !contains(mediaUsages, usage) {
					return usageError(fmt.Errorf("--usage must be one of: %s", strings.Join(mediaUsages, ", ")))
				}
				query.Set("usage", usage)
			}
			if favorite {
				query.Set("favorite", "1")
			}
			if sort != "" {
				if !contains(mediaSorts, sort) {
					return usageError(fmt.Errorf("--sort must be one of: %s", strings.Join(mediaSorts, ", ")))
				}
				query.Set("sort", sort)
			}
			for _, mimeType := range splitList(mimeTypes) {
				query.Add("mime_types[]", mimeType)
			}
			return list.run(cmd, env, "media", query, mediaColumns())
		},
	}
	list.maxSize = 100
	list.register(cmd)
	f := cmd.Flags()
	f.StringVar(&mediaType, "type", "", "only files of this kind: "+strings.Join(mediaTypes, ", "))
	f.StringVar(&folder, "folder", "", `only files in this folder, by uuid, name or path (Brand/Q4), or "root"`)
	f.StringVar(&keyword, "keyword", "", "only files whose name contains this")
	f.StringVar(&usage, "usage", "", "only files used this way: "+strings.Join(mediaUsages, ", "))
	f.BoolVar(&favorite, "favorite", false, "only files you starred")
	f.StringVar(&sort, "sort", "", "order: "+strings.Join(mediaSorts, ", ")+" (default newest)")
	f.StringArrayVar(&mimeTypes, "mime-type", nil, "only files of this MIME type, e.g. image/png; repeatable")
	cmd.RegisterFlagCompletionFunc("type", fixedCompletion(mediaTypes))
	cmd.RegisterFlagCompletionFunc("usage", fixedCompletion(mediaUsages))
	cmd.RegisterFlagCompletionFunc("sort", fixedCompletion(mediaSorts))
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
		folder       string
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
  mixpost media upload *.png --folder Brand -o id`,
		Args: minArgs(1, "mixpost media upload <file|url>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := parseSize(threshold)
			if err != nil {
				return err
			}
			var folderUUID string
			if folder != "" {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, folder)
				if err != nil {
					return err
				}
				if !root {
					folderUUID = uuid
				}
			}
			opts := mixpost.UploadOptions{
				AltText:     altText,
				Folder:      folderUUID,
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
					raw, err = uploadRemote(cmd, arg, opts, wait, pollInterval)
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
	f.StringVar(&folder, "folder", "", "folder to file the upload in, by uuid, name or path (default: the root)")
	f.BoolVar(&chunked, "chunked", false, "always use the chunked endpoints, whatever the file size")
	f.StringVar(&threshold, "chunk-threshold", "8MiB", "file size above which the chunked endpoints are used")
	f.IntVar(&concurrency, "concurrency", 3, "chunks uploaded in parallel")
	f.BoolVar(&wait, "wait", true, "wait for a remote download to finish")
	f.DurationVar(&pollInterval, "poll-interval", 2*time.Second, "how often to poll a remote download")
	return cmd
}

func uploadRemote(cmd *cobra.Command, fileURL string, opts mixpost.UploadOptions, wait bool, pollInterval time.Duration) ([]byte, error) {
	started, raw, err := env.Client.RemoteUploadInitiate(ctxOf(cmd), fileURL, opts)
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
	var name, altText, folder string

	cmd := &cobra.Command{
		Use:   "update <media-uuid>",
		Short: "Rename a media file, change its alt text, or file it in a folder",
		Example: `  mixpost media update 511b8c2d-... --alt-text "Team photo"
  mixpost media update 511b8c2d-... --name team.png --folder Brand
  mixpost media update 511b8c2d-... --folder root`,
		Args: exactArgs(1, "mixpost media update <media-uuid> --alt-text <text>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("alt-text") {
				body["alt_text"] = altText
			}
			if cmd.Flags().Changed("folder") {
				uuid, root, err := resolveFolder(ctxOf(cmd), env.Client, folder)
				if err != nil {
					return err
				}
				if root {
					body["folder"] = nil
				} else {
					body["folder"] = uuid
				}
			}
			if len(body) == 0 {
				return usageError(errors.New("nothing to change: pass --name, --alt-text or --folder"))
			}

			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPut, "media/"+args[0], nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, nil)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "new file name")
	f.StringVar(&altText, "alt-text", "", `alt text; "" clears it`)
	f.StringVar(&folder, "folder", "", `folder to file it in, by uuid, name or path, or "root"`)
	return cmd
}

// mediaDeleteBatch is the most media the API deletes in one request.
const mediaDeleteBatch = 500

func newMediaDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <media-id|media-uuid>...",
		Aliases: []string{"rm"},
		Short:   "Delete media files by numeric id or uuid",
		Long: `Delete media files and detach them from the posts that use them.

Files are named by numeric id or by uuid, mixed freely. Identifiers that match
nothing in the workspace are skipped by the API, so success does not prove that
anything was deleted.`,
		Example: `  mixpost media delete 12 13
  mixpost media list --usage unused -o id | xargs mixpost media delete --yes`,
		Args: minArgs(1, "mixpost media delete <media-id|media-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			items := make([]any, 0, len(args))
			for _, arg := range args {
				switch id, err := strconv.Atoi(arg); {
				case err == nil:
					items = append(items, id)
				case mixpost.LooksLikeUUID(arg):
					items = append(items, arg)
				default:
					return usageError(fmt.Errorf("%q is neither a numeric media id nor a uuid", arg))
				}
			}
			if err := env.Confirm(pluralPrompt("Delete", len(items), "media file")); err != nil {
				return err
			}

			for start := 0; start < len(items); start += mediaDeleteBatch {
				batch := items[start:min(start+mediaDeleteBatch, len(items))]
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodDelete, "media", nil, map[string]any{"items": batch})
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
