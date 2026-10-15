package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// postFlags collects everything the create and update commands can set on a
// post without the caller having to hand-write the JSON body.
type postFlags struct {
	file string

	texts     []string
	textFiles []string
	urls      []string
	media     []string

	accounts   []string
	tags       []string
	createTags bool

	at       string
	date     string
	timeOf   string
	timezone string

	schedule bool
	now      bool
	queue    bool

	options     []string
	optionsFile string
}

func (p *postFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&p.file, "file", "f", "", `JSON body to use as the base ("-" reads stdin); flags below override it`)

	f.StringArrayVar(&p.texts, "text", nil, "post text; repeat the flag to build a thread")
	f.StringArrayVar(&p.textFiles, "text-file", nil, `read post text from a file ("-" reads stdin); repeat for a thread`)
	f.StringArrayVar(&p.urls, "link", nil, "link attached to the matching text block (Facebook and LinkedIn)")
	f.StringArrayVar(&p.media, "media", nil, "media ids for the matching text block, comma separated; repeat per block")

	f.StringArrayVarP(&p.accounts, "account", "a", nil, "target account by id, uuid, username or provider; repeatable")
	f.StringArrayVarP(&p.tags, "tag", "t", nil, "tag by id, uuid or name; repeatable")
	f.BoolVar(&p.createTags, "create-tags", false, "create tags named with --tag when they do not exist")

	f.StringVar(&p.at, "at", "", `schedule time, e.g. "2026-09-10 15:00" or RFC 3339; implies --schedule`)
	f.StringVar(&p.date, "date", "", "schedule date as YYYY-MM-DD")
	f.StringVar(&p.timeOf, "time", "", "schedule time as HH:MM")
	f.StringVar(&p.timezone, "timezone", "", "IANA timezone for --at, --date and --time, e.g. Europe/Chisinau (default: your user timezone)")

	f.BoolVar(&p.schedule, "schedule", false, "schedule the post instead of leaving it a draft")
	f.BoolVar(&p.now, "now", false, "publish immediately; implies --schedule")
	f.BoolVar(&p.queue, "queue", false, "add the post to the publishing queue")

	f.StringArrayVar(&p.options, "option", nil, `per-provider option as path=value, e.g. --option mastodon.sensitive=true`)
	f.StringVar(&p.optionsFile, "options-file", "", "JSON file holding the options object")

	acceptTZShorthand(cmd)

	cmd.MarkFlagsMutuallyExclusive("at", "date")
	cmd.MarkFlagsMutuallyExclusive("at", "time")
	cmd.MarkFlagsMutuallyExclusive("now", "queue")
}

// build assembles the request body. base is the document read from --file, or
// the post being updated, and may be nil.
func (p *postFlags) build(ctx context.Context, cmd *cobra.Command, client *mixpost.Client, base map[string]any) (map[string]any, error) {
	body := base
	if body == nil {
		body = map[string]any{}
	} else if err := p.rebaseSchedule(cmd, body); err != nil {
		return nil, err
	}

	if p.file != "" {
		fromFile, err := readJSONObject(p.file)
		if err != nil {
			return nil, err
		}
		for key, value := range fromFile {
			body[key] = value
		}
	}

	if len(p.accounts) > 0 {
		ids, err := resolveAccountIDs(ctx, client, splitList(p.accounts))
		if err != nil {
			return nil, err
		}
		body["accounts"] = ids
	}

	if len(p.tags) > 0 {
		ids, err := resolveTagIDs(ctx, client, splitList(p.tags), p.createTags)
		if err != nil {
			return nil, err
		}
		body["tags"] = ids
	}

	if err := p.applySchedule(cmd, body); err != nil {
		return nil, err
	}

	content, err := p.buildContent()
	if err != nil {
		return nil, err
	}
	options, err := p.buildOptions()
	if err != nil {
		return nil, err
	}

	if content != nil || options != nil {
		versions, _ := asSlice(body["versions"])
		if len(versions) == 0 {
			versions = []any{map[string]any{}}
		}
		first, ok := asMap(versions[0])
		if !ok {
			return nil, usageError(fmt.Errorf("versions[0] in the base body is not an object"))
		}
		if _, exists := first["account_id"]; !exists {
			first["account_id"] = 0
		}
		if _, exists := first["is_original"]; !exists {
			first["is_original"] = true
		}
		if content != nil {
			first["content"] = content
		}
		if options != nil {
			merged, _ := asMap(first["options"])
			if merged == nil {
				merged = map[string]any{}
			}
			mergeMaps(merged, options)
			first["options"] = merged
		}
		versions[0] = first
		body["versions"] = versions
	}

	return body, nil
}

// rebaseSchedule prepares the schedule carried over from an existing post,
// which postToWriteBody writes in UTC. A flag that moves the post replaces the
// stored date and time together, so the old half is never read in another
// timezone, and the carried UTC gives way to the timezone of the new time.
func (p *postFlags) rebaseSchedule(cmd *cobra.Command, body map[string]any) error {
	changesDate := cmd.Flags().Changed("date")
	changesTime := cmd.Flags().Changed("time")
	_, scheduled := body["date"]

	switch {
	case p.at != "" || (changesDate && changesTime):
		delete(body, "timezone")
	case changesDate || changesTime:
		if scheduled {
			return usageError(errors.New("--date and --time move a scheduled post together; pass both, or --at"))
		}
	case p.timezone != "" && scheduled:
		return usageError(errors.New("--timezone says how to read --at, --date and --time; pass one of them to move the post"))
	}
	return nil
}

func (p *postFlags) applySchedule(cmd *cobra.Command, body map[string]any) error {
	date, timeOf, timezone := p.date, p.timeOf, p.timezone

	if p.at != "" {
		parsed, err := parseWhen(p.at)
		if err != nil {
			return err
		}
		if timezone, err = placeMoments(timezone, &parsed); err != nil {
			return err
		}
		date, timeOf = parsed.date(), parsed.clock()
	}

	if date != "" {
		body["date"] = date
	}
	if timeOf != "" {
		body["time"] = timeOf
	}
	if timezone != "" {
		body["timezone"] = timezone
	}

	switch {
	case p.now:
		body["schedule"] = true
		body["schedule_now"] = true
	case p.schedule || p.at != "" || cmd.Flags().Changed("date"):
		body["schedule"] = true
	case cmd.Flags().Changed("schedule"):
		body["schedule"] = p.schedule
	}
	if p.queue {
		body["queue"] = true
	}
	return nil
}

// buildContent turns the text, link and media flags into the content blocks of
// a version. It returns nil when no content flag was given, so an update can
// leave the existing content alone.
func (p *postFlags) buildContent() ([]any, error) {
	bodies := make([]string, 0, len(p.texts)+len(p.textFiles))
	bodies = append(bodies, p.texts...)
	for _, path := range p.textFiles {
		data, err := readInput(path)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, strings.TrimRight(string(data), "\n"))
	}

	blocks := max(len(bodies), len(p.urls), len(p.media))
	if blocks == 0 {
		return nil, nil
	}

	content := make([]any, 0, blocks)
	for i := range blocks {
		block := map[string]any{"body": ""}
		if i < len(bodies) {
			block["body"] = bodies[i]
		}
		if i < len(p.urls) {
			block["url"] = p.urls[i]
		}
		if i < len(p.media) {
			ids, err := parseIDs([]string{p.media[i]}, "media")
			if err != nil {
				return nil, err
			}
			block["media"] = ids
		}
		content = append(content, block)
	}
	return content, nil
}

func (p *postFlags) buildOptions() (map[string]any, error) {
	var options map[string]any

	if p.optionsFile != "" {
		loaded, err := readJSONObject(p.optionsFile)
		if err != nil {
			return nil, err
		}
		options = loaded
	}
	if len(p.options) == 0 {
		return options, nil
	}
	if options == nil {
		options = map[string]any{}
	}

	for _, pair := range p.options {
		path, raw, err := parseKeyValue(pair, "option")
		if err != nil {
			return nil, err
		}
		value, err := inferValue(raw)
		if err != nil {
			return nil, err
		}
		if err := setNested(options, path, value); err != nil {
			return nil, err
		}
	}
	return options, nil
}

func mergeMaps(dst, src map[string]any) {
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				mergeMaps(existing, nested)
				continue
			}
		}
		dst[key] = value
	}
}

// acceptTZShorthand lets --tz stand for --timezone.
func acceptTZShorthand(cmd *cobra.Command) {
	cmd.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "tz" {
			name = "timezone"
		}
		return pflag.NormalizedName(name)
	})
}

// resolveAccountIDs accepts numeric ids directly and looks anything else up
// against the workspace, matching a uuid, a username, a display name or a
// provider.
func resolveAccountIDs(ctx context.Context, client *mixpost.Client, specs []string) ([]int, error) {
	ids, pending := splitNumeric(specs)
	if len(pending) == 0 {
		return ids, nil
	}

	accounts, err := client.Accounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("look up accounts to resolve %s: %w", strings.Join(pending, ", "), err)
	}
	for _, spec := range pending {
		account, err := matchAccount(accounts, spec)
		if err != nil {
			return nil, err
		}
		ids = append(ids, account.ID)
	}
	return dedupe(ids), nil
}

// resolveAccountUUIDs is resolveAccountIDs for the endpoints that name
// accounts by uuid. A uuid costs no lookup.
func resolveAccountUUIDs(ctx context.Context, client *mixpost.Client, specs []string) ([]string, error) {
	var uuids, pending []string
	for _, spec := range specs {
		if mixpost.LooksLikeUUID(spec) {
			uuids = append(uuids, spec)
			continue
		}
		pending = append(pending, spec)
	}
	if len(pending) == 0 {
		return uuids, nil
	}

	accounts, err := client.Accounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("look up accounts to resolve %s: %w", strings.Join(pending, ", "), err)
	}
	for _, spec := range pending {
		account, err := matchAccount(accounts, spec)
		if err != nil {
			return nil, err
		}
		uuids = append(uuids, account.UUID)
	}
	return uuids, nil
}

// matchAccount finds the one account a spec names: its id, uuid, username,
// display name or provider.
func matchAccount(accounts []mixpost.Account, spec string) (mixpost.Account, error) {
	var matches []mixpost.Account
	for _, account := range accounts {
		if strconv.Itoa(account.ID) == spec ||
			account.UUID == spec ||
			strings.EqualFold(account.Username, spec) ||
			strings.EqualFold(account.Name, spec) ||
			strings.EqualFold(account.Provider, spec) {
			matches = append(matches, account)
		}
	}
	switch len(matches) {
	case 0:
		return mixpost.Account{}, usageError(fmt.Errorf("no account matches %q; available: %s", spec, describeAccounts(accounts)))
	case 1:
		return matches[0], nil
	}
	return mixpost.Account{}, usageError(fmt.Errorf("%q matches %d accounts (%s); use an id or a uuid", spec, len(matches), describeAccounts(matches)))
}

func describeAccounts(accounts []mixpost.Account) string {
	parts := make([]string, 0, len(accounts))
	for _, account := range accounts {
		label := account.Username
		if label == "" {
			label = account.Name
		}
		parts = append(parts, fmt.Sprintf("%d=%s/%s", account.ID, account.Provider, label))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// resolveTagIDs accepts numeric ids, uuids and names, optionally creating the
// names that do not exist yet.
func resolveTagIDs(ctx context.Context, client *mixpost.Client, specs []string, create bool) ([]int, error) {
	ids, pending := splitNumeric(specs)
	if len(pending) == 0 {
		return ids, nil
	}

	tags, err := client.Tags(ctx)
	if err != nil {
		return nil, fmt.Errorf("look up tags to resolve %s: %w", strings.Join(pending, ", "), err)
	}

	for _, spec := range pending {
		found := false
		for _, tag := range tags {
			if tag.UUID == spec || strings.EqualFold(tag.Name, spec) {
				ids = append(ids, tag.ID)
				found = true
				break
			}
		}
		if found {
			continue
		}
		if !create {
			return nil, usageError(fmt.Errorf("no tag named %q; pass --create-tags to create it", spec))
		}
		created, err := client.CreateTag(ctx, spec, defaultTagColor)
		if err != nil {
			return nil, fmt.Errorf("create tag %q: %w", spec, err)
		}
		ids = append(ids, created.ID)
		tags = append(tags, *created)
	}
	return dedupe(ids), nil
}

const defaultTagColor = "#60a5fa"

func splitNumeric(specs []string) (ids []int, rest []string) {
	for _, spec := range specs {
		if id, err := strconv.Atoi(spec); err == nil {
			ids = append(ids, id)
			continue
		}
		rest = append(rest, spec)
	}
	return ids, rest
}

func dedupe(ids []int) []int {
	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
