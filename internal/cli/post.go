package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

func postColumns() []output.Column {
	return []output.Column{
		{Header: "uuid", Path: "uuid"},
		{Header: "status", Path: "status"},
		{Header: "accounts", Path: "accounts[].username", MaxWidth: 24},
		{Header: "tags", Path: "tags[].name", MaxWidth: 20},
		{Header: "scheduled", Path: "scheduled_at"},
		{Header: "content", MaxWidth: 50, Fn: func(row any) string {
			return output.StripHTML(output.Get(row, "versions.0.content.0.body"))
		}},
	}
}

func newPostCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "post",
		Aliases: []string{"posts"},
		Short:   "Create, schedule and manage posts",
	}
	cmd.AddCommand(
		newPostListCommand(),
		newPostGetCommand(),
		newPostCreateCommand(),
		newPostUpdateCommand(),
		newPostScheduleCommand(),
		newPostQueueCommand(),
		newPostApproveCommand(),
		newPostDeleteCommand(),
	)
	return cmd
}

var (
	postStatuses        = []string{"draft", "needs_approval", "scheduled", "published", "failed", "trash"}
	postExcludeStatuses = []string{"draft", "scheduled", "published", "failed"}
)

func newPostListCommand() *cobra.Command {
	var (
		list          listFlags
		status        string
		excludeStatus string
		trashed       bool
		keyword       string
		accounts      []string
		tags          []string
		filters       []string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List posts",
		Long: `List posts in the current workspace, newest first.

Every filter is applied by the API, so it reaches the whole workspace rather
than the page in hand. --account and --tag match posts carrying any of the
given ones. --filter passes any other query parameter untouched.`,
		Example: `  mixpost post list --status scheduled --all
  mixpost post list --keyword launch --account linkedin
  mixpost post list --trashed -o id | xargs mixpost post delete --permanent --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := parseFilters(filters)
			if err != nil {
				return err
			}
			if query == nil {
				query = queryValues{}
			}

			if trashed {
				status = "trash"
			}
			if status != "" {
				if !contains(postStatuses, status) {
					return usageError(fmt.Errorf("--status must be one of: %s", strings.Join(postStatuses, ", ")))
				}
				query.Set("status", status)
			}
			if excludeStatus != "" {
				if !contains(postExcludeStatuses, excludeStatus) {
					return usageError(fmt.Errorf("--exclude-status must be one of: %s", strings.Join(postExcludeStatuses, ", ")))
				}
				query.Set("exclude_status", excludeStatus)
			}
			if keyword != "" {
				query.Set("keyword", keyword)
			}
			if len(accounts) > 0 {
				ids, err := resolveAccountIDs(ctxOf(cmd), env.Client, splitList(accounts))
				if err != nil {
					return err
				}
				for _, id := range ids {
					query.Add("accounts[]", itoa(id))
				}
			}
			if len(tags) > 0 {
				ids, err := resolveTagIDs(ctxOf(cmd), env.Client, splitList(tags), false)
				if err != nil {
					return err
				}
				for _, id := range ids {
					query.Add("tags[]", itoa(id))
				}
			}
			return list.run(cmd, env, "posts", query, postColumns())
		},
	}
	list.maxSize = 100
	list.register(cmd)
	f := cmd.Flags()
	f.StringVar(&status, "status", "", "only posts with this status: "+strings.Join(postStatuses, ", "))
	f.StringVar(&excludeStatus, "exclude-status", "", "leave out posts with this status: "+strings.Join(postExcludeStatuses, ", "))
	f.BoolVar(&trashed, "trashed", false, "only posts in the trash; same as --status trash")
	f.StringVar(&keyword, "keyword", "", "only posts whose text contains this")
	f.StringArrayVarP(&accounts, "account", "a", nil, "only posts to this account (id, uuid, username or provider); repeatable")
	f.StringArrayVarP(&tags, "tag", "t", nil, "only posts with this tag (id, uuid or name); repeatable")
	f.StringArrayVar(&filters, "filter", nil, "extra query parameter as key=value; repeatable")
	cmd.MarkFlagsMutuallyExclusive("status", "trashed")
	cmd.RegisterFlagCompletionFunc("status", fixedCompletion(postStatuses))
	cmd.RegisterFlagCompletionFunc("exclude-status", fixedCompletion(postExcludeStatuses))
	return cmd
}

func newPostGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <post-uuid>",
		Short: "Show one post",
		Args:  exactArgs(1, "mixpost post get <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "posts/"+args[0], nil)
			if err != nil {
				return err
			}
			return env.Render(resp, postColumns())
		},
	}
}

func newPostCreateCommand() *cobra.Command {
	var post postFlags

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a post, optionally scheduling or publishing it",
		Long: `Create a post.

Without any scheduling flag the post is left as a draft. Repeat --text to build
a thread: each occurrence becomes one content block, and --link and --media
line up with the blocks in the same order.`,
		Example: `  mixpost post create --text "Hello world" --account twitter --schedule --at "2026-09-10 15:00"
  mixpost post create --text "Part 1" --text "Part 2" --account 1 --account 2 --now
  mixpost post create --text "Launch" --media 4,5 --tag launch --create-tags
  cat post.json | mixpost post create -f -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := post.build(ctxOf(cmd), cmd, env.Client, nil)
			if err != nil {
				return err
			}
			if _, ok := body["versions"]; !ok {
				return usageError(fmt.Errorf("a post needs content: pass --text, --text-file, --media or --file"))
			}

			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "posts", nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, postColumns())
		},
	}
	post.register(cmd)
	return cmd
}

func newPostUpdateCommand() *cobra.Command {
	var (
		post         postFlags
		fromExisting bool
	)

	cmd := &cobra.Command{
		Use:   "update <post-uuid>",
		Short: "Change a post's content, accounts, tags or schedule",
		Long: `Update a post.

The API replaces the whole post, so by default the current post is fetched
first and the flags are applied on top of it, keeping it at the same moment.
Pass --from-existing=false to send only what the flags describe.

A scheduled post moves with --at, or with --date and --time together, read in
--timezone or your user timezone. A scheduled post is checked again against
what its platforms require.`,
		Example: `  mixpost post update 8eb4e98d-... --text "Corrected text"
  mixpost post update 8eb4e98d-... --at "2026-09-11 09:00" --timezone Europe/Chisinau`,
		Args: exactArgs(1, "mixpost post update <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var base map[string]any
			if fromExisting {
				resp, err := env.Client.Get(ctxOf(cmd), "posts/"+args[0], nil)
				if err != nil {
					return err
				}
				if resp.Status != 0 {
					var current map[string]any
					if err := resp.DecodeInto(&current); err != nil {
						return fmt.Errorf("decode post %s: %w", args[0], err)
					}
					base = postToWriteBody(current)
				}
			}

			body, err := post.build(ctxOf(cmd), cmd, env.Client, base)
			if err != nil {
				return err
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPut, "posts/"+args[0], nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, postColumns())
		},
	}
	post.register(cmd)
	cmd.Flags().BoolVar(&fromExisting, "from-existing", true, "read the post first and apply the flags on top of it")
	return cmd
}

func newPostScheduleCommand() *cobra.Command {
	var (
		now  bool
		when scheduleFlags
	)

	cmd := &cobra.Command{
		Use:   "schedule <post-uuid>...",
		Short: "Schedule a post, or publish it immediately with --now",
		Long: `Move a post out of draft.

Without a time the post goes out at the date and time already stored on it.
--at, or --date with --time, moves it as it is scheduled. --account-at gives an
account a time of its own; accounts without one publish with the post.

The post is checked against what each of its platforms requires first, and a
422 lists every requirement it does not meet.`,
		Example: `  mixpost post schedule 8eb4e98d-...
  mixpost post schedule 8eb4e98d-... --now
  mixpost post schedule 8eb4e98d-... --at "2026-09-10 15:00" --tz Europe/Chisinau
  mixpost post schedule 8eb4e98d-... --at "2026-09-10 15:00" --account-at linkedin="2026-09-10 18:00"`,
		Args: minArgs(1, "mixpost post schedule <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if now && when.changed(cmd) {
				return usageError(errors.New("--now publishes at once; it cannot be combined with a time"))
			}
			body, err := when.build(ctxOf(cmd), cmd, env.Client)
			if err != nil {
				return err
			}
			if body == nil {
				body = map[string]any{}
			}
			body["postNow"] = now

			for _, uuid := range args {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "posts/schedule/"+uuid, nil, body)
				if err != nil {
					return err
				}
				if err := env.Render(resp, postColumns()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&now, "now", false, "publish immediately instead of at the stored time")
	when.register(cmd)
	return cmd
}

func newPostQueueCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "queue <post-uuid>...",
		Aliases: []string{"add-to-queue"},
		Short:   "Add posts to the publishing queue",
		Args:    minArgs(1, "mixpost post queue <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, uuid := range args {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "posts/add-to-queue/"+uuid, nil, nil)
				if err != nil {
					return err
				}
				if err := env.Render(resp, postColumns()); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newPostApproveCommand() *cobra.Command {
	var when scheduleFlags

	cmd := &cobra.Command{
		Use:   "approve <post-uuid>...",
		Short: "Approve posts that are waiting for review",
		Long: `Approve posts that are waiting for review, scheduling them.

Without a time a post keeps the date and time it carries. --at, or --date with
--time, moves it as it is approved, and --account-at gives an account a time of
its own.`,
		Example: `  mixpost post approve 8eb4e98d-...
  mixpost post approve 8eb4e98d-... --at "2026-09-11 09:00" --tz Europe/Chisinau`,
		Args: minArgs(1, "mixpost post approve <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := when.build(ctxOf(cmd), cmd, env.Client)
			if err != nil {
				return err
			}

			for _, uuid := range args {
				var payload any
				if body != nil {
					payload = body
				}
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "posts/approve/"+uuid, nil, payload)
				if err != nil {
					return err
				}
				if err := env.Render(resp, postColumns()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	when.register(cmd)
	return cmd
}

var deleteModes = []string{"app_only", "app_and_social", "social_only"}

func newPostDeleteCommand() *cobra.Command {
	var (
		permanent  bool
		deleteMode string
	)

	cmd := &cobra.Command{
		Use:     "delete <post-uuid>...",
		Aliases: []string{"rm"},
		Short:   "Move posts to the trash, or delete them permanently",
		Long: `Delete posts.

Posts go to the trash by default. --permanent removes them for good and asks
for confirmation first; in a script, pass --yes as well.`,
		Example: `  mixpost post delete 8eb4e98d-...
  mixpost post delete 8eb4e98d-... b1b06706-... --permanent --yes
  mixpost post delete 8eb4e98d-... --permanent --delete-mode app_and_social --yes`,
		Args: minArgs(1, "mixpost post delete <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !contains(deleteModes, deleteMode) {
				return usageError(fmt.Errorf("--delete-mode must be one of: %s", strings.Join(deleteModes, ", ")))
			}
			if permanent {
				if err := env.Confirm(pluralPrompt("Permanently delete", len(args), "post")); err != nil {
					return err
				}
			}

			body := map[string]any{"trash": !permanent, "delete_mode": deleteMode}
			if len(args) == 1 {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodDelete, "posts/"+args[0], nil, body)
				if err != nil {
					return err
				}
				return env.Render(resp, nil)
			}

			body["posts"] = args
			return deleteMany(ctxOf(cmd), env, "posts", body)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&permanent, "permanent", false, "delete permanently instead of moving to the trash")
	f.StringVar(&deleteMode, "delete-mode", "app_only", "where to delete from: "+strings.Join(deleteModes, ", "))
	cmd.RegisterFlagCompletionFunc("delete-mode", fixedCompletion(deleteModes))
	return cmd
}

// postToWriteBody converts a post as the API returns it into a body the API
// accepts: related records collapse to their ids, and the stored schedule is
// carried over so an edit does not silently unschedule the post.
func postToWriteBody(post map[string]any) map[string]any {
	body := map[string]any{}

	if accounts, ok := asSlice(post["accounts"]); ok {
		body["accounts"] = idsOf(accounts)
	}
	if tags, ok := asSlice(post["tags"]); ok {
		body["tags"] = idsOf(tags)
	}

	if versions, ok := asSlice(post["versions"]); ok {
		rebuilt := make([]any, 0, len(versions))
		for _, item := range versions {
			version, ok := asMap(item)
			if !ok {
				continue
			}
			clean := map[string]any{
				"account_id":  version["account_id"],
				"is_original": version["is_original"],
			}
			// A version that follows another stops following when an update
			// leaves synced_with out.
			if synced, ok := version["synced_with"]; ok && synced != nil {
				clean["synced_with"] = synced
			}
			if options, ok := asMap(version["options"]); ok {
				clean["options"] = options
			}
			if content, ok := asSlice(version["content"]); ok {
				blocks := make([]any, 0, len(content))
				for _, contentItem := range content {
					block, ok := asMap(contentItem)
					if !ok {
						continue
					}
					rebuiltBlock := map[string]any{"body": block["body"]}
					if url, ok := block["url"]; ok {
						rebuiltBlock["url"] = url
					}
					if media, ok := asSlice(block["media"]); ok {
						rebuiltBlock["media"] = idsOf(media)
					}
					blocks = append(blocks, rebuiltBlock)
				}
				clean["content"] = blocks
			}
			rebuilt = append(rebuilt, clean)
		}
		body["versions"] = rebuilt
	}

	// scheduled_at comes back in UTC, while a date and time sent without a
	// timezone are read in the user's timezone, so the timezone goes with them
	// to keep the post at the same moment.
	if scheduled := output.Stringify(post["scheduled_at"]); scheduled != "" {
		if date, timeOf, ok := strings.Cut(scheduled, " "); ok {
			body["date"] = date
			if len(timeOf) >= 5 {
				body["time"] = timeOf[:5]
			}
			body["timezone"] = "UTC"
		}
	}
	return body
}

// idsOf collects the id of every record in a list, accepting the string ids the
// media resource uses.
func idsOf(items []any) []int {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		record, ok := asMap(item)
		if !ok {
			if id, ok := toInt(item); ok {
				ids = append(ids, id)
			}
			continue
		}
		if id, ok := toInt(record["id"]); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func parseFilters(pairs []string) (queryValues, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	values := queryValues{}
	for _, pair := range pairs {
		key, value, err := parseKeyValue(pair, "filter")
		if err != nil {
			return nil, err
		}
		values.Add(key, value)
	}
	return values, nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// fixedCompletion completes a flag from a fixed list of values.
func fixedCompletion(values []string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
