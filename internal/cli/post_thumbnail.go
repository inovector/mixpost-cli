package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// buildThumbnails reads --thumbnail and --thumbnail-at into one cover per
// video, keyed by the video's media id. A video given both flags gets both: the
// image for the networks that take one, the moment for those that only take a
// frame of the video. It returns nil when neither flag was given.
func (p *postFlags) buildThumbnails() (map[int]map[string]any, error) {
	if len(p.thumbnails) == 0 && len(p.thumbnailsAt) == 0 {
		return nil, nil
	}

	covers := map[int]map[string]any{}
	coverOf := func(videoID int) map[string]any {
		if covers[videoID] == nil {
			covers[videoID] = map[string]any{"media_id": videoID}
		}
		return covers[videoID]
	}

	for _, pair := range splitList(p.thumbnails) {
		videoID, raw, err := parseVideoPair(pair, "thumbnail")
		if err != nil {
			return nil, err
		}
		imageID, err := strconv.Atoi(raw)
		if err != nil {
			return nil, usageError(fmt.Errorf("--thumbnail expects video_id=image_id, got %q", pair))
		}
		coverOf(videoID)["thumb_id"] = imageID
	}

	for _, pair := range splitList(p.thumbnailsAt) {
		videoID, raw, err := parseVideoPair(pair, "thumbnail-at")
		if err != nil {
			return nil, err
		}
		milliseconds, err := parseMoment(raw)
		if err != nil {
			return nil, usageError(fmt.Errorf("--thumbnail-at expects video_id=time in ms or a duration like 2.5s, got %q", pair))
		}
		coverOf(videoID)["timestamp_ms"] = milliseconds
	}

	return covers, nil
}

func parseVideoPair(pair, flagName string) (int, string, error) {
	key, value, err := parseKeyValue(pair, flagName)
	if err != nil {
		return 0, "", err
	}
	videoID, err := strconv.Atoi(key)
	if err != nil {
		return 0, "", usageError(fmt.Errorf("--%s expects a numeric video id before =, got %q", flagName, pair))
	}
	return videoID, strings.TrimSpace(value), nil
}

// parseMoment reads a bare number as milliseconds and anything else as a Go
// duration, so 2500, 2.5s and 1m2s all work.
func parseMoment(raw string) (int, error) {
	if milliseconds, err := strconv.Atoi(raw); err == nil {
		if milliseconds < 0 {
			return 0, fmt.Errorf("negative time")
		}
		return milliseconds, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("invalid time %q", raw)
	}
	return int(duration.Milliseconds()), nil
}

// carryThumbnails keeps the covers of the replaced content for every video the
// new content still posts, in whichever block now holds it. Without this,
// rewriting the text of a post would take its video covers away.
func carryThumbnails(previous any, content []any) {
	covers := map[int]any{}
	blocks, _ := asSlice(previous)
	for _, item := range blocks {
		block, ok := asMap(item)
		if !ok {
			continue
		}
		entries, _ := asSlice(block["video_thumbs"])
		for _, entry := range entries {
			cover, ok := asMap(entry)
			if !ok {
				continue
			}
			if videoID, ok := toInt(cover["media_id"]); ok {
				covers[videoID] = cover
			}
		}
	}
	if len(covers) == 0 {
		return
	}

	for _, item := range content {
		block, ok := asMap(item)
		if !ok {
			continue
		}
		var kept []any
		for _, videoID := range mediaIDsOf(block) {
			if cover, ok := covers[videoID]; ok {
				kept = append(kept, cover)
			}
		}
		if kept != nil {
			block["video_thumbs"] = kept
		}
	}
}

// applyThumbnails sets each cover on the block that posts its video, replacing
// any cover that video had.
func applyThumbnails(content any, covers map[int]map[string]any) error {
	blocks, _ := asSlice(content)

	videoIDs := make([]int, 0, len(covers))
	for videoID := range covers {
		videoIDs = append(videoIDs, videoID)
	}
	sort.Ints(videoIDs)

	for _, videoID := range videoIDs {
		block := blockPosting(blocks, videoID)
		if block == nil {
			return usageError(fmt.Errorf("video %d is not in the post's media: add it with --media, or pick a video the post already has", videoID))
		}

		entries, _ := asSlice(block["video_thumbs"])
		kept := make([]any, 0, len(entries)+1)
		for _, entry := range entries {
			cover, ok := asMap(entry)
			if id, isID := toInt(cover["media_id"]); ok && isID && id == videoID {
				continue
			}
			kept = append(kept, entry)
		}
		block["video_thumbs"] = append(kept, covers[videoID])
	}
	return nil
}

func blockPosting(blocks []any, videoID int) map[string]any {
	for _, item := range blocks {
		block, ok := asMap(item)
		if !ok {
			continue
		}
		for _, id := range mediaIDsOf(block) {
			if id == videoID {
				return block
			}
		}
	}
	return nil
}

// mediaIDsOf reads a block's media ids, whether they were built from flags,
// carried over from the post as ints, or read from a JSON file.
func mediaIDsOf(block map[string]any) []int {
	switch media := block["media"].(type) {
	case []int:
		return media
	case []any:
		ids := make([]int, 0, len(media))
		for _, item := range media {
			if id, ok := toInt(item); ok {
				ids = append(ids, id)
			}
		}
		return ids
	}
	return nil
}
