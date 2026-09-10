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
    "content": [{"body": "<div>Hello</div>", "url": "", "media": [{"id": "5", "name": "a.png"}]}],
    "options": {"mastodon": {"sensitive": false}}
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

	if body["date"] != "2026-03-29" || body["time"] != "09:50" {
		t.Errorf("schedule = %v %v, want the stored scheduled_at", body["date"], body["time"])
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
	}{
		{"2026-09-10 15:00", "2026-09-10", "15:00"},
		{"2026-09-10T15:00", "2026-09-10", "15:00"},
		{"2026-09-10 15:00:30", "2026-09-10", "15:00"},
		{"2026-09-10", "2026-09-10", "00:00"},
		{"2026-09-10T15:00:00Z", "2026-09-10", "15:00"},
	}
	for _, c := range cases {
		got, err := parseWhen(c.in)
		if err != nil {
			t.Errorf("parseWhen(%q): %v", c.in, err)
			continue
		}
		if got.Format("2006-01-02") != c.date || got.Format("15:04") != c.time {
			t.Errorf("parseWhen(%q) = %s, want %s %s", c.in, got, c.date, c.time)
		}
	}

	if _, err := parseWhen("next tuesday"); err == nil {
		t.Error("expected an error for an unparsable time")
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
