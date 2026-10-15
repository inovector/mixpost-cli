package cli

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

// mentionProviders are the network families a mention can hold an identity
// for. One identity serves every variant of a network: linkedin covers
// profiles and pages alike.
var mentionProviders = []string{"twitter", "facebook_page", "instagram", "threads", "mastodon", "pixelfed", "linkedin", "tiktok", "bluesky"}

func mentionColumns() []output.Column {
	return []output.Column{
		{Header: "id", Path: "id"},
		{Header: "name", Path: "name", MaxWidth: 30},
		{Header: "insert", Fn: func(row any) string {
			reference := output.Stringify(output.Get(row, "reference"))
			if reference == "" {
				return ""
			}
			return mentionToken(reference)
		}},
		{Header: "identities", MaxWidth: 50, Fn: func(row any) string {
			identities, _ := output.Get(row, "identities").([]any)
			parts := make([]string, 0, len(identities))
			for _, identity := range identities {
				parts = append(parts, output.Stringify(output.Get(identity, "provider"))+":"+output.Stringify(output.Get(identity, "handle")))
			}
			return strings.Join(parts, ", ")
		}},
	}
}

// mentionToken is what a post body carries to mention someone; each account
// receives it as the handle or structured reference its network expects.
func mentionToken(reference string) string {
	return "{{mention:" + reference + "}}"
}

func newMentionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mention",
		Aliases: []string{"mentions"},
		Short:   "Manage the people and brands a workspace mentions",
		Long: `Manage mentions.

A mention holds the handle a person or brand goes by on each network. A post
mentions them by writing {{mention:<reference>}} in its text, which every
account receives as the identity its own network expects: a handle where the
network reads plain text, a structured reference where it does not. A network
the mention has no identity for receives the mention's name as plain text.

The INSERT column of 'mention list' is the text to paste into a post.`,
	}
	cmd.AddCommand(
		newMentionListCommand(),
		newMentionGetCommand(),
		newMentionCreateCommand(),
		newMentionUpdateCommand(),
		newMentionDeleteCommand(),
	)
	return cmd
}

func newMentionListCommand() *cobra.Command {
	var (
		list   listFlags
		search string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List mentions, most used first",
		Example: `  mixpost mention list --search ana
  mixpost post create --text "Thanks {{mention:42}} for the review" --account twitter --account bluesky`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := queryValues{}
			if search != "" {
				query.Set("q", search)
			}
			return list.run(cmd, env, "mentions", query, mentionColumns())
		},
	}
	list.sizeParam = "per_page"
	list.maxSize = 100
	list.register(cmd)
	cmd.Flags().StringVar(&search, "search", "", "only mentions whose name or a handle starts with this")
	return cmd
}

func newMentionGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <mention-id>",
		Short: "Show one mention and its identities",
		Args:  exactArgs(1, "mixpost mention get <mention-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "mentions/"+args[0], nil)
			if err != nil {
				return err
			}
			return env.Render(resp, mentionColumns())
		},
	}
}

// mentionFlags describe a mention's name and identities. An identity is
// written provider=handle; its network id and label go in separate flags,
// because ids such as Bluesky DIDs carry characters a combined syntax would
// have to escape.
type mentionFlags struct {
	file       string
	name       string
	avatar     string
	identities []string
	ids        []string
	labels     []string
	remove     []string
}

func (m *mentionFlags) register(cmd *cobra.Command, updating bool) {
	f := cmd.Flags()
	f.StringVarP(&m.file, "file", "f", "", `JSON body to use as the base ("-" reads stdin); flags below override it`)
	f.StringVar(&m.name, "name", "", "display name, also the plain text a network without an identity receives")
	f.StringVar(&m.avatar, "avatar", "", "avatar image URL")
	f.StringArrayVar(&m.identities, "identity", nil, "handle on one network as provider=handle, e.g. bluesky=ana.bsky.social; repeatable")
	f.StringArrayVar(&m.ids, "provider-id", nil, "the network's own id for the profile as provider=id (a DID on Bluesky, a URN on LinkedIn, a numeric id on X); repeatable")
	f.StringArrayVar(&m.labels, "label", nil, "display name on one network as provider=label, where it differs; repeatable")
	if updating {
		f.StringArrayVar(&m.remove, "remove-identity", nil, "drop the identity on this network; repeatable")
	}
}

// apply writes the flags onto a mention body. base is the mention being
// updated, or nil.
func (m *mentionFlags) apply(cmd *cobra.Command, base map[string]any) (map[string]any, error) {
	body := map[string]any{}
	identities := map[string]map[string]any{}
	var order []string

	addIdentity := func(raw any) {
		identity, ok := asMap(raw)
		if !ok {
			return
		}
		provider := output.Stringify(identity["provider"])
		if provider == "" {
			return
		}
		clean := map[string]any{"provider": provider, "handle": identity["handle"]}
		for _, key := range []string{"provider_id", "label"} {
			if value, ok := identity[key]; ok && value != nil {
				clean[key] = value
			}
		}
		if _, seen := identities[provider]; !seen {
			order = append(order, provider)
		}
		identities[provider] = clean
	}

	if base != nil {
		for _, key := range []string{"name", "avatar"} {
			if value, ok := base[key]; ok && value != nil {
				body[key] = value
			}
		}
		if list, ok := asSlice(base["identities"]); ok {
			for _, identity := range list {
				addIdentity(identity)
			}
		}
	}
	if m.file != "" {
		doc, err := readJSONObject(m.file)
		if err != nil {
			return nil, err
		}
		for key, value := range doc {
			if key == "identities" {
				identities, order = map[string]map[string]any{}, nil
				if list, ok := asSlice(value); ok {
					for _, identity := range list {
						addIdentity(identity)
					}
				}
				continue
			}
			body[key] = value
		}
	}

	if cmd.Flags().Changed("name") {
		body["name"] = m.name
	}
	if cmd.Flags().Changed("avatar") {
		body["avatar"] = m.avatar
	}

	setField := func(pairs []string, flag, field string) error {
		for _, pair := range pairs {
			provider, value, err := parseKeyValue(pair, flag)
			if err != nil {
				return err
			}
			if !contains(mentionProviders, provider) {
				return usageError(fmt.Errorf("--%s: unknown network %q; use one of: %s", flag, provider, strings.Join(mentionProviders, ", ")))
			}
			identity, ok := identities[provider]
			if !ok {
				if field != "handle" {
					return usageError(fmt.Errorf("--%s %s: the mention has no %s identity; add one with --identity %s=<handle>", flag, pair, provider, provider))
				}
				identity = map[string]any{"provider": provider}
				identities[provider] = identity
				order = append(order, provider)
			}
			if field == "handle" {
				value = strings.TrimPrefix(value, "@")
			}
			identity[field] = value
		}
		return nil
	}
	if err := setField(m.identities, "identity", "handle"); err != nil {
		return nil, err
	}
	if err := setField(m.ids, "provider-id", "provider_id"); err != nil {
		return nil, err
	}
	if err := setField(m.labels, "label", "label"); err != nil {
		return nil, err
	}
	for _, provider := range splitList(m.remove) {
		if _, ok := identities[provider]; !ok {
			return nil, usageError(fmt.Errorf("--remove-identity: the mention has no %s identity", provider))
		}
		delete(identities, provider)
	}

	list := make([]any, 0, len(identities))
	for _, provider := range order {
		if identity, ok := identities[provider]; ok {
			list = append(list, identity)
		}
	}
	if base != nil || len(list) > 0 || m.file != "" {
		body["identities"] = list
	}
	return body, nil
}

func newMentionCreateCommand() *cobra.Command {
	var mention mentionFlags

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a mention and the handles it goes by",
		Example: `  mixpost mention create --name "Ana Popescu" \
    --identity bluesky=ana.bsky.social --provider-id bluesky=did:plc:abcdef123456 \
    --identity twitter=anapopescu --provider-id twitter=1234567890`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := mention.apply(cmd, nil)
			if err != nil {
				return err
			}
			if output.Stringify(body["name"]) == "" {
				return usageError(errors.New("a mention needs a name: pass --name"))
			}
			resp, err := env.Client.Call(ctxOf(cmd), http.MethodPost, "mentions", nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, mentionColumns())
		},
	}
	mention.register(cmd, false)
	registerProviderCompletion(cmd)
	return cmd
}

func newMentionUpdateCommand() *cobra.Command {
	var mention mentionFlags

	cmd := &cobra.Command{
		Use:   "update <mention-id>",
		Short: "Rename a mention or change its handles",
		Long: `Update a mention.

The API replaces the mention's identities with the ones sent, so the mention is
read first and the flags are applied on top of it: --identity adds or changes
one network, and --remove-identity drops one. A network left without an
identity receives the mention as plain text.`,
		Example: `  mixpost mention update 8f7c1e2b-... --identity threads=ana.popescu
  mixpost mention update 8f7c1e2b-... --remove-identity twitter`,
		Args: exactArgs(1, "mixpost mention update <mention-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "mentions/"+args[0], nil)
			if err != nil {
				return err
			}
			current := map[string]any{}
			if resp.Status != 0 {
				if err := resp.DecodeInto(&current); err != nil {
					return fmt.Errorf("decode mention %s: %w", args[0], err)
				}
			}

			body, err := mention.apply(cmd, current)
			if err != nil {
				return err
			}
			resp, err = env.Client.Call(ctxOf(cmd), http.MethodPut, "mentions/"+args[0], nil, body)
			if err != nil {
				return err
			}
			return env.Render(resp, mentionColumns())
		},
	}
	mention.register(cmd, true)
	registerProviderCompletion(cmd)
	return cmd
}

func newMentionDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <mention-id>...",
		Aliases: []string{"rm"},
		Short:   "Delete mentions",
		Long: `Delete mentions and their identities.

Posts that reference a deleted mention keep reading as written: the mention
falls back to the plain text stored with it.`,
		Args: minArgs(1, "mixpost mention delete <mention-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Confirm(pluralPrompt("Delete", len(args), "mention")); err != nil {
				return err
			}
			for _, id := range args {
				resp, err := env.Client.Call(ctxOf(cmd), http.MethodDelete, "mentions/"+id, nil, nil)
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

func registerProviderCompletion(cmd *cobra.Command) {
	providers := append([]string(nil), mentionProviders...)
	sort.Strings(providers)
	withEquals := make([]string, len(providers))
	for i, provider := range providers {
		withEquals[i] = provider + "="
	}
	for _, name := range []string{"identity", "provider-id", "label"} {
		cmd.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return withEquals, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		})
	}
	if cmd.Flags().Lookup("remove-identity") != nil {
		cmd.RegisterFlagCompletionFunc("remove-identity", fixedCompletion(providers))
	}
}
