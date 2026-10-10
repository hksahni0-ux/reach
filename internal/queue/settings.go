package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The Settings tab lets Prateek steer a run from the Sheet:
//
//	A Topic | B Include (tick box) | C Drafts per run | D Platforms     | F Draft a comment for these posts
//	CRM     | TRUE                 | 3                | LinkedIn        | https://www.linkedin.com/posts/…
//
// Topics missing from the tab keep the config's defaults; a missing tab changes nothing.
const (
	SettingsTab   = "Settings"
	settingsRange = SettingsTab + "!A2:F"
)

// Platform choices in column D.
const (
	BothPlatforms = "LinkedIn + YouTube"
	LinkedInOnly  = "LinkedIn"
	YouTubeOnly   = "YouTube"
)

// TopicSetting is one topic's row.
type TopicSetting struct {
	Include  bool
	Drafts   int // 0 means "use the run's default"
	LinkedIn bool
	YouTube  bool
}

// Settings is what the tab asks for this run.
type Settings struct {
	Topics map[string]TopicSetting // by topic name, as in the config
	Posts  []string                // specific posts or videos to draft for, whatever their numbers
}

// ReadSettings reads the Settings tab. ok is false if the tab doesn't exist yet.
func (s Sheet) ReadSettings(ctx context.Context) (Settings, bool, error) {
	raw, err := s.Ex.Execute(ctx, getSlug, map[string]any{"spreadsheet_id": s.ID, "ranges": []string{settingsRange}})
	if err != nil {
		if strings.Contains(err.Error(), "Unable to parse range") {
			return Settings{}, false, nil
		}
		return Settings{}, false, err
	}
	st, err := settingsIn(raw)
	return st, err == nil, err
}

func settingsIn(raw json.RawMessage) (Settings, error) {
	type valueRange struct {
		Values [][]string `json:"values"`
	}
	var r struct {
		ValueRanges  []valueRange `json:"valueRanges"`
		ResponseData *struct {
			ValueRanges []valueRange `json:"valueRanges"`
		} `json:"response_data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Settings{}, fmt.Errorf("parsing the Settings tab: %w", err)
	}
	vr := r.ValueRanges
	if len(vr) == 0 && r.ResponseData != nil {
		vr = r.ResponseData.ValueRanges
	}
	st := Settings{Topics: map[string]TopicSetting{}}
	if len(vr) == 0 {
		return st, nil
	}
	for _, row := range vr[0].Values {
		cell := func(c int) string {
			if c < len(row) {
				return strings.TrimSpace(row[c])
			}
			return ""
		}
		if name := cell(0); name != "" {
			n, _ := strconv.Atoi(cell(2))
			ts := TopicSetting{Include: strings.EqualFold(cell(1), "TRUE"), Drafts: max(n, 0), LinkedIn: true, YouTube: true}
			switch cell(3) {
			case LinkedInOnly:
				ts.YouTube = false
			case YouTubeOnly:
				ts.LinkedIn = false
			}
			st.Topics[name] = ts
		}
		if u := cell(5); strings.HasPrefix(u, "https://") {
			st.Posts = append(st.Posts, u)
		}
	}
	return st, nil
}
