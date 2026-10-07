package queue

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hksahni0-ux/reach/internal/draft"
	"github.com/hksahni0-ux/reach/internal/rank"
	"github.com/hksahni0-ux/reach/internal/rules"
)

func TestRowMatchesHeader(t *testing.T) {
	r := Row(rank.Scored{}, draft.Draft{Comment: "hi"}, time.Now())
	if len(r) != len(Header) {
		t.Fatalf("row has %d columns, header %d", len(r), len(Header))
	}
	if r[1] != "pending" {
		t.Fatalf("status = %v, want pending", r[1])
	}
	bad := Row(rank.Scored{}, draft.Draft{Violations: []rules.Violation{{Rule: "too long", Detail: "x"}}}, time.Now())
	if bad[1] != "needs edit" || bad[14] == "" {
		t.Fatalf("failed draft should be flagged, got %v / %v", bad[1], bad[14])
	}
}

func TestURLsIn(t *testing.T) {
	raw := json.RawMessage(`{"valueRanges":[{"values":[["https://www.linkedin.com/posts/a-activity-1"],["https://www.linkedin.com/posts/b-activity-2"]]}]}`)
	got := urlsIn(raw)
	if len(got) != 2 || !got["https://www.linkedin.com/posts/a-activity-1"] {
		t.Fatalf("got %v", got)
	}
}
