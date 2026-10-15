package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

var (
	analyticsPeriods = []string{"this_week", "last_week", "this_month", "last_month", "last_3_months", "this_year"}
	analyticsTypes   = []string{"overview", "engagement", "content", "hashtags", "insights", "audience", "reach", "video", "competitors", "search_terms", "reviews"}
	weekdays         = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
)

func newAnalyticsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Read follower, reach and engagement figures",
		Long: `Read the analytics shown on the dashboard's analytics pages.

Periods are UTC days. A figure compared with the previous period of the same
length comes as {value, change}, where change is a percentage. Every role can
read analytics, VIEWER included.

JSON output is the API response untouched; the table is a digest of it.`,
	}
	cmd.AddCommand(
		newAnalyticsSummaryCommand(),
		newAnalyticsAccountCommand(),
		newAnalyticsPostCommand(),
		newAnalyticsPostingTimesCommand(),
		newAnalyticsBestTimesCommand(),
	)
	return cmd
}

// periodFlags choose the days analytics cover: a named period, or a custom
// range that wins over it.
type periodFlags struct {
	period string
	from   string
	to     string
}

func (p *periodFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&p.period, "period", "", "reporting period: "+strings.Join(analyticsPeriods, ", ")+" (default this_month)")
	f.StringVar(&p.from, "from", "", "first day of a custom range, YYYY-MM-DD (UTC); needs --to")
	f.StringVar(&p.to, "to", "", "last day of a custom range, YYYY-MM-DD (UTC); needs --from")
	cmd.RegisterFlagCompletionFunc("period", fixedCompletion(analyticsPeriods))
}

func (p *periodFlags) query() (queryValues, error) {
	q := queryValues{}
	if p.period != "" {
		if !contains(analyticsPeriods, p.period) {
			return nil, usageError(fmt.Errorf("--period must be one of: %s", strings.Join(analyticsPeriods, ", ")))
		}
		q.Set("period", p.period)
	}
	if (p.from == "") != (p.to == "") {
		return nil, usageError(errors.New("--from and --to make a range together; pass both"))
	}
	if p.from != "" {
		q.Set("date_from", p.from)
		q.Set("date_to", p.to)
	}
	return q, nil
}

func standingColumns() []output.Column {
	return []output.Column{
		{Header: "account", Path: "account.name", MaxWidth: 30},
		{Header: "provider", Path: "account.provider"},
		{Header: "followers", Path: "followers"},
		{Header: "growth", Path: "follower_growth"},
		{Header: "growth %", Path: "growth_rate"},
		{Header: "posts", Path: "posts"},
	}
}

func newAnalyticsSummaryCommand() *cobra.Command {
	var period periodFlags

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Workspace-wide followers, growth and posting across every account",
		Long: `Show the workspace's analytics for a period, across every account that
supports analytics. The table lists the follower leaderboard; JSON adds the
daily audience chart, the best and least growing accounts and the posting
streak.`,
		Example: `  mixpost analytics summary
  mixpost analytics summary --period last_month
  mixpost analytics summary --from 2026-01-01 --to 2026-03-31 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := period.query()
			if err != nil {
				return err
			}
			resp, err := env.Client.Get(ctxOf(cmd), "analytics", query)
			if err != nil {
				return err
			}
			noteDigest(resp, func(doc any) string {
				return fmt.Sprintf("%s · followers %s (%s) · posts %s (%s) · posted on %s of %s days",
					describePeriod(doc),
					output.Stringify(output.Get(doc, "data.total_followers")),
					describeChange(output.Get(doc, "data.follower_growth")),
					output.Stringify(output.Get(doc, "data.total_posts.value")),
					describePercent(output.Get(doc, "data.total_posts.change")),
					output.Stringify(output.Get(doc, "data.posting_streak.active_days")),
					output.Stringify(output.Get(doc, "data.posting_streak.total_days")),
				)
			})
			return env.RenderWith(resp, output.RowsAt("data.leaderboard"), standingColumns())
		},
	}
	period.register(cmd)
	return cmd
}

func newAnalyticsAccountCommand() *cobra.Command {
	var (
		period  periodFlags
		kind    string
		page    int
		perPage int
		sortBy  string
		sortDir string
	)

	cmd := &cobra.Command{
		Use:   "account <account>",
		Short: "One type of analytics for one account",
		Long: `Show one type of analytics for a single account. The account is named by
uuid, id, username, display name or provider.

Each platform supports its own types, and the response lists them in "tabs";
without --type the account's default (overview) is returned. The shape of
"data" depends on the platform and the type, so the table shows its headline
figures and leaves the charts to JSON.`,
		Example: `  mixpost analytics account twitter
  mixpost analytics account acme --type content --sort-by IMPRESSION_COUNT
  mixpost analytics account ed1d7f93-... --type audience --period last_3_months -o json`,
		Args: exactArgs(1, "mixpost analytics account <account>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := period.query()
			if err != nil {
				return err
			}
			if kind != "" {
				if !contains(analyticsTypes, kind) {
					return usageError(fmt.Errorf("--type must be one of: %s", strings.Join(analyticsTypes, ", ")))
				}
				query.Set("type", kind)
			}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			if sortBy != "" {
				query.Set("sort_by", sortBy)
			}
			if sortDir != "" {
				if sortDir != "asc" && sortDir != "desc" {
					return usageError(errors.New("--sort-dir must be asc or desc"))
				}
				query.Set("sort_dir", sortDir)
			}

			uuids, err := resolveAccountUUIDs(ctxOf(cmd), env.Client, []string{args[0]})
			if err != nil {
				return err
			}
			resp, err := env.Client.Get(ctxOf(cmd), "analytics/accounts/"+uuids[0], query)
			if err != nil {
				return err
			}
			noteDigest(resp, func(doc any) string {
				note := fmt.Sprintf("%s · %s · %s · types: %s",
					output.Stringify(output.Get(doc, "account.name")),
					output.Stringify(output.Get(doc, "type")),
					describePeriod(doc),
					output.Stringify(output.Get(doc, "tabs")),
				)
				if output.Stringify(output.Get(doc, "initial_sync.in_progress")) == "true" {
					note += " · still importing history, figures are incomplete"
				}
				return note
			})
			return env.RenderWith(resp, analyticsFigures, []output.Column{
				{Header: "metric", Path: "metric"},
				{Header: "value", Path: "value"},
				{Header: "change %", Path: "change"},
			})
		},
	}
	period.register(cmd)
	f := cmd.Flags()
	f.StringVar(&kind, "type", "", "analytics type: "+strings.Join(analyticsTypes, ", "))
	f.IntVar(&page, "page", 0, "page of posts or reviews, for --type content and reviews")
	f.IntVar(&perPage, "per-page", 0, "posts or reviews per page, up to 50")
	f.StringVar(&sortBy, "sort-by", "", "metric to sort content posts by, e.g. IMPRESSION_COUNT (default: newest first)")
	f.StringVar(&sortDir, "sort-dir", "", "direction for --sort-by: asc or desc (default desc)")
	cmd.RegisterFlagCompletionFunc("sort-dir", fixedCompletion([]string{"asc", "desc"}))
	cmd.RegisterFlagCompletionFunc("type", fixedCompletion(analyticsTypes))
	return cmd
}

// analyticsFigures picks the headline figures of an account's analytics: the
// scalars and {value, change} pairs at the top of "data". Charts and lists are
// left to JSON, since their shape varies by platform.
func analyticsFigures(doc any) []any {
	data, _ := output.Get(doc, "data").(map[string]any)
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]any, 0, len(keys))
	for _, key := range keys {
		switch value := data[key].(type) {
		case map[string]any:
			if figure, ok := value["value"]; ok {
				if _, nested := figure.(map[string]any); !nested {
					rows = append(rows, map[string]any{"metric": key, "value": figure, "change": value["change"]})
				}
			}
		case []any, nil:
		default:
			rows = append(rows, map[string]any{"metric": key, "value": value})
		}
	}
	return rows
}

func newAnalyticsPostCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "post <post-uuid>",
		Short: "How a published post performed on each account",
		Long: `Show how a published post performed on every account it went out to: each
account's metrics as its platform reports them, its engagement rate, and how
the post compares with that account's average post. JSON adds the totals,
daily trends and the winning account.`,
		Example: `  mixpost analytics post 8eb4e98d-...`,
		Args:    exactArgs(1, "mixpost analytics post <post-uuid>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := env.Client.Get(ctxOf(cmd), "posts/"+args[0]+"/analytics", nil)
			if err != nil {
				return err
			}
			noteDigest(resp, func(doc any) string {
				if _, ok := output.Get(doc, "summary.totals").(map[string]any); !ok {
					return "The post has not been published to any account."
				}
				return fmt.Sprintf("views %s · likes %s · comments %s · shares %s · engagement %s%%",
					output.Stringify(output.Get(doc, "summary.totals.views")),
					output.Stringify(output.Get(doc, "summary.totals.likes")),
					output.Stringify(output.Get(doc, "summary.totals.comments")),
					output.Stringify(output.Get(doc, "summary.totals.shares")),
					output.Stringify(output.Get(doc, "summary.engagement_rate")),
				)
			})
			return env.RenderWith(resp, output.RowsAt("accounts"), []output.Column{
				{Header: "account", Path: "account_id"},
				{Header: "engagement %", Path: "engagement_rate"},
				{Header: "vs average", Path: "vs_average.engagement"},
				{Header: "peak day", Path: "peak_day.date"},
				{Header: "metrics", MaxWidth: 60, Fn: func(row any) string {
					metrics, _ := output.Get(row, "metrics").([]any)
					parts := make([]string, 0, len(metrics))
					for _, metric := range metrics {
						parts = append(parts, output.Stringify(output.Get(metric, "label"))+"="+output.Stringify(output.Get(metric, "value")))
					}
					return strings.Join(parts, " ")
				}},
			})
		},
	}
}

// timingFlags pick the accounts and timezone the posting-time endpoints score.
type timingFlags struct {
	accounts []string
	timezone string
}

func (t *timingFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVarP(&t.accounts, "account", "a", nil, "account to consider (uuid, id, username or provider); repeatable (default: every account)")
	f.StringVar(&t.timezone, "timezone", "", "IANA timezone for days and hours (default: your user timezone)")
	acceptTZShorthand(cmd)
}

func (t *timingFlags) query(cmd *cobra.Command, client *mixpost.Client) (queryValues, error) {
	q := queryValues{}
	if len(t.accounts) > 0 {
		uuids, err := resolveAccountUUIDs(ctxOf(cmd), client, splitList(t.accounts))
		if err != nil {
			return nil, err
		}
		for _, uuid := range uuids {
			q.Add("accounts[]", uuid)
		}
	}
	if t.timezone != "" {
		q.Set("timezone", t.timezone)
	}
	return q, nil
}

func newAnalyticsPostingTimesCommand() *cobra.Command {
	var timing timingFlags

	cmd := &cobra.Command{
		Use:   "posting-times",
		Short: "How well each hour of the week has worked, from past posts",
		Long: `Score each hour of the week by how the past posts of the chosen accounts
performed. Scores run from 0 to 100, where 50 is neutral, and every account
weighs the same. The table lists the best slots; JSON adds the score of every
weekday and of each 2-hour interval.`,
		Example: `  mixpost analytics posting-times
  mixpost analytics posting-times --account linkedin --tz Europe/Chisinau`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := timing.query(cmd, env.Client)
			if err != nil {
				return err
			}
			resp, err := env.Client.Get(ctxOf(cmd), "analytics/posting-times", query)
			if err != nil {
				return err
			}
			noteDigest(resp, func(doc any) string {
				if output.Stringify(output.Get(doc, "has_data")) == "false" {
					return "None of the accounts has scored history yet."
				}
				return fmt.Sprintf("%s · %s posts across %s accounts",
					output.Stringify(output.Get(doc, "timezone")),
					output.Stringify(output.Get(doc, "samples")),
					output.Stringify(output.Get(doc, "accounts")),
				)
			})
			return env.RenderWith(resp, output.RowsAt("best"), []output.Column{
				{Header: "day", Fn: func(row any) string { return weekdayName(output.Get(row, "day")) }},
				{Header: "hour", Fn: func(row any) string {
					hour, ok := toInt(output.Get(row, "hour"))
					if !ok {
						return ""
					}
					return fmt.Sprintf("%02d:00", hour)
				}},
				{Header: "score", Path: "score"},
				{Header: "posts", Path: "samples"},
			})
		},
	}
	timing.register(cmd)
	return cmd
}

func newAnalyticsBestTimesCommand() *cobra.Command {
	var (
		timing timingFlags
		from   string
	)

	cmd := &cobra.Command{
		Use:   "best-times",
		Short: "The next best date and time to post, for each account",
		Long: `Show, for each account, the next time its own best slot comes round within
the following 14 days. Times are never in the past and are written in the
timezone shown, ready to pass to 'post create --at' with the same --timezone.
An account whose history does not point to a best time yet is left out.`,
		Example: `  mixpost analytics best-times --tz Europe/Chisinau
  mixpost analytics best-times --account linkedin --from 2026-10-20`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := timing.query(cmd, env.Client)
			if err != nil {
				return err
			}
			if from != "" {
				query.Set("from", from)
			}
			resp, err := env.Client.Get(ctxOf(cmd), "analytics/best-times", query)
			if err != nil {
				return err
			}
			noteDigest(resp, func(doc any) string {
				return "Times are in " + output.Stringify(output.Get(doc, "timezone")) + "."
			})
			return env.RenderWith(resp, output.RowsAt("times"), []output.Column{
				{Header: "account", Path: "account_id"},
				{Header: "account_uuid", Path: "account_uuid"},
				{Header: "at", Fn: func(row any) string {
					return output.Stringify(output.Get(row, "date")) + " " + output.Stringify(output.Get(row, "time"))
				}},
			})
		},
	}
	timing.register(cmd)
	cmd.Flags().StringVar(&from, "from", "", "day to look from, YYYY-MM-DD in the timezone (default: now)")
	return cmd
}

// noteDigest writes a one-line digest of a response to stderr above a table,
// for what the table's rows leave out. Structured output carries the whole
// response, so it gets no note.
func noteDigest(resp *mixpost.Response, digest func(doc any) string) {
	if resp == nil || resp.Status == 0 || env.Printer.Format != output.FormatTable {
		return
	}
	var doc any
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return
	}
	if note := digest(doc); note != "" {
		env.Printer.PrintMessage("%s", note)
	}
}

func describePeriod(doc any) string {
	return output.Stringify(output.Get(doc, "period.start_date")) + " – " + output.Stringify(output.Get(doc, "period.end_date"))
}

// describeChange spells a {value, change} figure as "+10, +12.5%".
func describeChange(figure any) string {
	value := output.Stringify(output.Get(figure, "value"))
	if n, err := strconv.ParseFloat(value, 64); err == nil && n > 0 {
		value = "+" + value
	}
	return value + ", " + describePercent(output.Get(figure, "change"))
}

func describePercent(change any) string {
	text := output.Stringify(change)
	if text == "" {
		return "no previous figure"
	}
	if n, err := strconv.ParseFloat(text, 64); err == nil && n > 0 {
		text = "+" + text
	}
	return text + "%"
}

func weekdayName(v any) string {
	day, ok := toInt(v)
	if !ok || day < 0 || day >= len(weekdays) {
		return output.Stringify(v)
	}
	return weekdays[day]
}
