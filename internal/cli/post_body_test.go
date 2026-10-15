package cli

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A post as the API returns it: accounts, tags and media are objects, and the
// media id is a string.
const existingPost = `{
  "id": 1,
  "uuid": "post-uuid",
  "status": "scheduled",
  "accounts": [{"id": 2, "username": "acme"}, {"id": 3, "username": "acme2"}],
  "tags": [{"id": 7, "name": "launch"}],
  "versions": [{
    "account_id": 0,
    "is_original": true,
    "synced_with": null,
    "content": [{"body": "<div>Hello</div>", "url": "", "media": [{"id": "5", "name": "a.png"}]}],
    "options": {"mastodon": {"sensitive": false}}
  }, {
    "account_id": 3,
    "is_original": false,
    "synced_with": 0,
    "content": [{"body": "<div>Hello</div>", "url": "", "media": []}],
    "options": {}
  }],
  "scheduled_at": "2026-03-29 09:50:56",
  "user": {"name": "Someone"},
  "trashed": false
}`

func TestPostToWriteBodyCollapsesRelationsToIDs(t *testing.T) {
	var post map[string]any
	if err := json.Unmarshal([]byte(existingPost), &post); err != nil {
		t.Fatal(err)
	}

	body := postToWriteBody(post)

	if got := body["accounts"]; !reflect.DeepEqual(got, []int{2, 3}) {
		t.Errorf("accounts = %v, want [2 3]", got)
	}
	if got := body["tags"]; !reflect.DeepEqual(got, []int{7}) {
		t.Errorf("tags = %v, want [7]", got)
	}

	versions := body["versions"].([]any)
	version := versions[0].(map[string]any)
	block := version["content"].([]any)[0].(map[string]any)
	if got := block["media"]; !reflect.DeepEqual(got, []int{5}) {
		t.Errorf("media = %v, want [5]: the API returns media ids as strings", got)
	}
	if version["options"] == nil {
		t.Error("options were dropped")
	}
	if _, present := version["synced_with"]; present {
		t.Error("a version with content of its own should not gain synced_with")
	}
	if synced := versions[1].(map[string]any)["synced_with"]; synced != float64(0) {
		t.Errorf("synced_with = %v, want 0: an update without it stops the version following the original", synced)
	}

	if body["date"] != "2026-03-29" || body["time"] != "09:50" {
		t.Errorf("schedule = %v %v, want the stored scheduled_at", body["date"], body["time"])
	}
	if body["timezone"] != "UTC" {
		t.Errorf("timezone = %v, want UTC: scheduled_at is UTC, and a date sent without a timezone is read in the user's", body["timezone"])
	}
	for _, key := range []string{"id", "uuid", "status", "user", "trashed", "scheduled_at"} {
		if _, present := body[key]; present {
			t.Errorf("%q is read-only and should not be sent back", key)
		}
	}
}

func TestPostToWriteBodyWithoutASchedule(t *testing.T) {
	var post map[string]any
	if err := json.Unmarshal([]byte(`{"scheduled_at": null, "versions": []}`), &post); err != nil {
		t.Fatal(err)
	}
	body := postToWriteBody(post)
	if _, present := body["date"]; present {
		t.Error("a draft must not gain a schedule date")
	}
}

func TestParseWhen(t *testing.T) {
	cases := []struct {
		in         string
		date, time string
		absolute   bool
	}{
		{"2026-09-10 15:00", "2026-09-10", "15:00", false},
		{"2026-09-10T15:00", "2026-09-10", "15:00", false},
		{"2026-09-10 15:00:30", "2026-09-10", "15:00", false},
		{"2026-09-10", "2026-09-10", "00:00", false},
		{"2026-09-10T15:00:00Z", "2026-09-10", "15:00", true},
		{"2026-09-10T15:00:00+03:00", "2026-09-10", "15:00", true},
	}
	for _, c := range cases {
		got, err := parseWhen(c.in)
		if err != nil {
			t.Errorf("parseWhen(%q): %v", c.in, err)
			continue
		}
		if got.date() != c.date || got.clock() != c.time || got.absolute != c.absolute {
			t.Errorf("parseWhen(%q) = %s %s absolute=%v, want %s %s absolute=%v", c.in, got.date(), got.clock(), got.absolute, c.date, c.time, c.absolute)
		}
	}

	if _, err := parseWhen("next tuesday"); err == nil {
		t.Error("expected an error for an unparsable time")
	}
}

func TestPlaceMomentsConvertsOffsetsIntoTheRequestTimezone(t *testing.T) {
	// An instant with an offset and no --timezone is sent in UTC.
	offset, _ := parseWhen("2026-09-10T15:00:00+03:00")
	tz, err := placeMoments("", &offset)
	if err != nil || tz != "UTC" || offset.clock() != "12:00" {
		t.Errorf("no timezone: tz=%q clock=%s err=%v, want UTC 12:00", tz, offset.clock(), err)
	}

	// With --timezone, the instant is written in that timezone.
	offset, _ = parseWhen("2026-09-10T12:00:00Z")
	wall, _ := parseWhen("2026-09-10 18:00")
	tz, err = placeMoments("Europe/Chisinau", &offset, &wall)
	if err != nil || tz != "Europe/Chisinau" || offset.clock() != "15:00" || wall.clock() != "18:00" {
		t.Errorf("with timezone: tz=%q offset=%s wall=%s err=%v", tz, offset.clock(), wall.clock(), err)
	}

	// Wall-clock times are read in a timezone the CLI does not know, so they
	// cannot be mixed with instants unless --timezone names it.
	offset, _ = parseWhen("2026-09-10T12:00:00Z")
	wall, _ = parseWhen("2026-09-10 18:00")
	if _, err := placeMoments("", &offset, &wall); err == nil {
		t.Error("mixing an instant and a wall-clock time without a timezone should fail")
	}
}

func TestInferValueTypesScalars(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"true", true},
		{"false", false},
		{"null", nil},
		{"42", int64(42)},
		{"1.5", 1.5},
		{"PUBLIC_TO_EVERYONE", "PUBLIC_TO_EVERYONE"},
		{`{"a":1}`, map[string]any{"a": float64(1)}},
	}
	for _, c := range cases {
		got, err := inferValue(c.in)
		if err != nil {
			t.Errorf("inferValue(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("inferValue(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestSetNestedBuildsIntermediateObjects(t *testing.T) {
	doc := map[string]any{}
	if err := setNested(doc, "tiktok.privacy_level.account-2", "SELF_ONLY"); err != nil {
		t.Fatalf("setNested: %v", err)
	}
	tiktok := doc["tiktok"].(map[string]any)
	privacy := tiktok["privacy_level"].(map[string]any)
	if privacy["account-2"] != "SELF_ONLY" {
		t.Errorf("doc = %v", doc)
	}
}

func TestSetNestedRefusesToOverwriteAScalar(t *testing.T) {
	doc := map[string]any{"youtube": "not an object"}
	if err := setNested(doc, "youtube.title", "x"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMergeMapsIsDeep(t *testing.T) {
	dst := map[string]any{"mastodon": map[string]any{"sensitive": false, "keep": 1}}
	mergeMaps(dst, map[string]any{"mastodon": map[string]any{"sensitive": true}})

	mastodon := dst["mastodon"].(map[string]any)
	if mastodon["sensitive"] != true {
		t.Errorf("sensitive = %v, want true", mastodon["sensitive"])
	}
	if mastodon["keep"] != 1 {
		t.Errorf("the merge dropped a sibling key: %v", mastodon)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"8MiB", 8 << 20},
		{"8MB", 8 << 20},
		{"512K", 512 << 10},
		{"1GiB", 1 << 30},
		{"1024", 1024},
		{"1.5MiB", 1024 * 1024 * 3 / 2},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := parseSize("big"); err == nil {
		t.Error("expected an error")
	}
}

func TestDedupeKeepsOrder(t *testing.T) {
	if got := dedupe([]int{3, 1, 3, 2, 1}); !reflect.DeepEqual(got, []int{3, 1, 2}) {
		t.Errorf("dedupe = %v", got)
	}
}

func TestIdsOfAcceptsBareNumbers(t *testing.T) {
	var items []any
	if err := json.Unmarshal([]byte(`[1, {"id": 2}, {"id": "3"}]`), &items); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("idsOf = %v", got)
	}
}
