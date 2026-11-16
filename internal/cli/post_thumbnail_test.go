package cli

import (
	"reflect"
	"strings"
	"testing"
)

const videoPostUUID = "8eb4e98d-0000-4000-8000-000000000002"

// A post whose video has a cover given both ways, ids as the API returns them.
const postWithVideoCover = `{
  "uuid": "8eb4e98d-0000-4000-8000-000000000002",
  "status": "draft",
  "accounts": [{"id": 7}],
  "tags": [],
  "versions": [
    {"account_id": 0, "is_original": true, "synced_with": null, "options": {},
     "content": [{"body": "Reel", "media": [{"id": "4"}], "video_thumbs": [{"media_id": "4", "thumb_id": "9", "timestamp_ms": 2500}]}]}
  ],
  "scheduled_at": null
}`

func firstBlock(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	version := body["versions"].([]any)[0].(map[string]any)
	return version["content"].([]any)[0].(map[string]any)
}

func TestPostCreateSetsAVideoCoverBothWays(t *testing.T) {
	api := newFakeAPI(map[string]string{"POST /posts": `{"uuid":"new-post"}`})

	_, stderr, code := run(t, api,
		"post", "create",
		"--text", "Reel",
		"--media", "4",
		"--thumbnail", "4=9",
		"--thumbnail-at", "4=2.5s",
	)
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	got := firstBlock(t, api.last(t, "POST", "/posts").Body)["video_thumbs"]
	want := []any{map[string]any{"media_id": float64(4), "thumb_id": float64(9), "timestamp_ms": float64(2500)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("video_thumbs = %v, want %v", got, want)
	}
}

func TestThumbnailAtReadsMillisecondsAndDurations(t *testing.T) {
	for raw, want := range map[string]int{"2500": 2500, "2.5s": 2500, "1m2s": 62000, "1500ms": 1500, "0": 0} {
		got, err := parseMoment(raw)
		if err != nil || got != want {
			t.Errorf("parseMoment(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-1", "soon", "-2s"} {
		if _, err := parseMoment(raw); err == nil {
			t.Errorf("parseMoment(%q) should fail", raw)
		}
	}
}

func TestThumbnailForAVideoThePostDoesNotHaveIsAUsageError(t *testing.T) {
	api := newFakeAPI(map[string]string{"POST /posts": `{}`})

	_, stderr, code := run(t, api, "post", "create", "--text", "Reel", "--media", "4", "--thumbnail", "5=9")

	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "video 5 is not in the post's media") {
		t.Errorf("stderr = %q", stderr)
	}
	if n := api.count("POST", "/posts"); n != 0 {
		t.Errorf("%d requests were sent for a cover that cannot be placed", n)
	}
}

func TestPostUpdateKeepsTheVideoCover(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/" + videoPostUUID: postWithVideoCover,
		"PUT /posts/" + videoPostUUID: `{}`,
	})

	_, stderr, code := run(t, api, "post", "update", videoPostUUID, "--option", "mastodon.sensitive=true")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	got := firstBlock(t, api.last(t, "PUT", "/posts/"+videoPostUUID).Body)["video_thumbs"]
	want := []any{map[string]any{"media_id": "4", "thumb_id": "9", "timestamp_ms": float64(2500)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("video_thumbs = %v, want the stored cover: the API replaces the whole post", got)
	}
}

func TestPostUpdateKeepsTheCoverOfAVideoTheNewContentStillPosts(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/" + videoPostUUID: postWithVideoCover,
		"PUT /posts/" + videoPostUUID: `{}`,
	})

	_, stderr, code := run(t, api, "post", "update", videoPostUUID, "--text", "New caption", "--media", "4")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	covers, _ := firstBlock(t, api.last(t, "PUT", "/posts/"+videoPostUUID).Body)["video_thumbs"].([]any)
	if len(covers) != 1 {
		t.Fatalf("video_thumbs = %v, want the cover kept", covers)
	}
}

func TestPostUpdateReplacesTheCoverOfOneVideo(t *testing.T) {
	api := newFakeAPI(map[string]string{
		"GET /posts/" + videoPostUUID: postWithVideoCover,
		"PUT /posts/" + videoPostUUID: `{}`,
	})

	_, stderr, code := run(t, api, "post", "update", videoPostUUID, "--thumbnail-at", "4=1200")
	if code != ExitOK {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr)
	}

	got := firstBlock(t, api.last(t, "PUT", "/posts/"+videoPostUUID).Body)["video_thumbs"]
	want := []any{map[string]any{"media_id": float64(4), "timestamp_ms": float64(1200)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("video_thumbs = %v, want only the new cover", got)
	}
}
