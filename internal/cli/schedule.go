package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	// Embedded so --timezone resolves on systems without a zoneinfo database.
	_ "time/tzdata"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/spf13/cobra"
)

// moment is a time read from the command line. A wall-clock time such as
// "2026-09-10 15:00" is sent as written, for the server to read in the
// request's timezone. An absolute one, RFC 3339 with an offset or "now", names
// an instant, and is converted into whatever timezone the request carries.
type moment struct {
	t        time.Time
	absolute bool
}

func (m moment) date() string  { return m.t.Format("2006-01-02") }
func (m moment) clock() string { return m.t.Format("15:04") }

// whenLayouts are the accepted spellings of a wall-clock time, from most to
// least specific.
var whenLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"15:04",
}

func parseWhen(value string) (moment, error) {
	trimmed := strings.TrimSpace(value)
	if strings.EqualFold(trimmed, "now") {
		return moment{t: time.Now(), absolute: true}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return moment{t: parsed, absolute: true}, nil
	}
	for _, layout := range whenLayouts {
		parsed, err := time.Parse(layout, trimmed)
		if err != nil {
			continue
		}
		if layout == "15:04" {
			now := time.Now()
			parsed = time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
		}
		return moment{t: parsed}, nil
	}
	return moment{}, usageError(fmt.Errorf(`cannot read %q as a time; use "2006-01-02 15:04", "2006-01-02", "15:04" or RFC 3339`, value))
}

// placeMoments converts the absolute moments into the timezone the request is
// read in, and returns the timezone to send. An empty result leaves it to the
// server, which reads the request in the user's timezone.
func placeMoments(timezone string, moments ...*moment) (string, error) {
	var hasAbsolute, hasWall bool
	for _, m := range moments {
		if m.absolute {
			hasAbsolute = true
		} else {
			hasWall = true
		}
	}
	if !hasAbsolute {
		return timezone, nil
	}

	loc := time.UTC
	switch {
	case timezone != "":
		loaded, err := time.LoadLocation(timezone)
		if err != nil {
			return "", usageError(fmt.Errorf("unknown timezone %q", timezone))
		}
		loc = loaded
	case hasWall:
		// The wall-clock times will be read in the user's timezone, which the
		// CLI does not know, so the absolute ones have nothing to convert to.
		return "", usageError(errors.New(`times with an offset (or "now") and times without one cannot be mixed unless --timezone says how to read the latter`))
	default:
		timezone = "UTC"
	}
	for _, m := range moments {
		if m.absolute {
			m.t = m.t.In(loc)
		}
	}
	return timezone, nil
}

// scheduleFlags set when an existing post goes out, for the schedule and
// approve commands, whose bodies share date, time, timezone and the
// per-account times.
type scheduleFlags struct {
	at       string
	date     string
	timeOf   string
	timezone string

	accountTimes      []string
	clearAccountTimes bool
}

func (s *scheduleFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&s.at, "at", "", `when the post goes out, e.g. "2026-09-10 15:00" or RFC 3339`)
	f.StringVar(&s.date, "date", "", "date as YYYY-MM-DD; needs --time")
	f.StringVar(&s.timeOf, "time", "", "time as HH:MM; needs --date")
	f.StringVar(&s.timezone, "timezone", "", "IANA timezone the times are written in (default: your user timezone)")
	f.StringArrayVar(&s.accountTimes, "account-at", nil, `when one account publishes, as account=time, e.g. --account-at linkedin="2026-09-10 18:00"; repeatable`)
	f.BoolVar(&s.clearAccountTimes, "clear-account-times", false, "drop the per-account times so every account publishes with the post")
	acceptTZShorthand(cmd)

	cmd.MarkFlagsMutuallyExclusive("at", "date")
	cmd.MarkFlagsMutuallyExclusive("at", "time")
	cmd.MarkFlagsMutuallyExclusive("account-at", "clear-account-times")
}

func (s *scheduleFlags) changed(cmd *cobra.Command) bool {
	for _, name := range []string{"at", "date", "time", "timezone", "account-at", "clear-account-times"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// build returns the schedule part of the request body, or nil when no flag
// was given, so the post keeps the time it already carries.
func (s *scheduleFlags) build(ctx context.Context, cmd *cobra.Command, client *mixpost.Client) (map[string]any, error) {
	if !s.changed(cmd) {
		return nil, nil
	}
	if (s.date == "") != (s.timeOf == "") {
		return nil, usageError(errors.New("--date and --time go together; pass both, or --at"))
	}

	var moments []*moment
	var postTime *moment
	switch {
	case s.at != "":
		parsed, err := parseWhen(s.at)
		if err != nil {
			return nil, err
		}
		postTime = &parsed
	case s.date != "":
		parsed, err := parseWhen(s.date + " " + s.timeOf)
		if err != nil {
			return nil, err
		}
		postTime = &parsed
	}
	if postTime != nil {
		moments = append(moments, postTime)
	}

	type accountTime struct {
		id   int
		when *moment
	}
	var perAccount []accountTime
	for _, pair := range s.accountTimes {
		spec, value, err := parseKeyValue(pair, "account-at")
		if err != nil {
			return nil, err
		}
		ids, err := resolveAccountIDs(ctx, client, []string{spec})
		if err != nil {
			return nil, err
		}
		parsed, err := parseWhen(value)
		if err != nil {
			return nil, err
		}
		perAccount = append(perAccount, accountTime{id: ids[0], when: &parsed})
		moments = append(moments, &parsed)
	}

	timezone, err := placeMoments(s.timezone, moments...)
	if err != nil {
		return nil, err
	}

	body := map[string]any{}
	if postTime != nil {
		body["date"] = postTime.date()
		body["time"] = postTime.clock()
	}
	if timezone != "" {
		body["timezone"] = timezone
	}
	switch {
	case s.clearAccountTimes:
		body["accounts_schedule"] = []any{}
	case len(perAccount) > 0:
		entries := make([]any, 0, len(perAccount))
		for _, entry := range perAccount {
			entries = append(entries, map[string]any{
				"account_id": entry.id,
				"date":       entry.when.date(),
				"time":       entry.when.clock(),
			})
		}
		body["accounts_schedule"] = entries
	}
	return body, nil
}
