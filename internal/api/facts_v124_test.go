package api

// facts_v124_test.go — v1.24 facts: the winning version's bitrate and
// the item's free-form tags. Both feed client-side judges (bitrate
// family; rule-based custom badges' tag condition).

import (
	"testing"
)

const fixtureBitrateTwoVersions = `{
  "type": "movie", "title": "Two Versions",
  "Media": [
    { "width": 1920, "height": 1080, "videoResolution": "1080", "bitrate": 8000,
      "Part": [{"file": "/movies/two-versions-1080.mkv", "Stream": [
        {"streamType": 1, "codec": "h264"},
        {"streamType": 2, "codec": "ac3", "channels": 6, "languageCode": "eng"}
      ]}] },
    { "width": 3840, "height": 2160, "videoResolution": "4k", "bitrate": 25000,
      "Part": [{"file": "/movies/two-versions-2160.mkv", "Stream": [
        {"streamType": 1, "codec": "hevc"},
        {"streamType": 2, "codec": "truehd", "channels": 8, "languageCode": "eng"}
      ]}] }
  ]
}`

func TestStreamFactsBitrateRidesWithWinner(t *testing.T) {
	sf := parseStreamFacts(fixtureFull(t, fixtureBitrateTwoVersions))
	if sf.ResolutionTier != "4K" {
		t.Fatalf("tier = %q, want 4K", sf.ResolutionTier)
	}
	// The bitrate must be the WINNING (4K) version's, not the 1080p
	// version's 8000 — a lower-tier version never overwrites it.
	if sf.BitrateKbps != 25000 {
		t.Errorf("BitrateKbps = %d, want 25000 (the winning version's)", sf.BitrateKbps)
	}
}

const fixtureBitrateUnstated = `{
  "type": "movie", "title": "No Bitrate",
  "Media": [
    { "width": 1920, "height": 1080, "videoResolution": "1080",
      "Part": [{"file": "/movies/no-bitrate.mkv", "Stream": [
        {"streamType": 1, "codec": "h264"}
      ]}] }
  ]
}`

func TestStreamFactsBitrateUnstated(t *testing.T) {
	sf := parseStreamFacts(fixtureFull(t, fixtureBitrateUnstated))
	if sf.BitrateKbps != 0 {
		t.Errorf("BitrateKbps = %d, want 0 (Plex states none — the judge must stay dark, not guess)", sf.BitrateKbps)
	}
}

func TestTagsOf(t *testing.T) {
	full := fixtureFull(t, `{
	  "type": "movie", "title": "Tagged",
	  "Tag": [{"tag": "Criterion"}, {"tag": ""}, {"tag": "  "}, {"tag": "Staff Pick"}],
	  "Genre": [{"tag": "Drama"}]
	}`)
	got := tagsOf(full)
	if len(got) != 2 || got[0] != "Criterion" || got[1] != "Staff Pick" {
		t.Errorf("tagsOf = %v, want [Criterion Staff Pick] in Plex order, empty tags dropped", got)
	}
	// A missing Tag array yields an empty slice, never nil-panic fuel.
	empty := tagsOf(fixtureFull(t, `{"type": "movie"}`))
	if len(empty) != 0 {
		t.Errorf("tagsOf (no Tag array) = %v, want empty", empty)
	}
}
