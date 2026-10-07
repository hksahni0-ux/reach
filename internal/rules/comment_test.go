package rules

import (
	"strings"
	"testing"
)

var policy = Policy{MaxChars: 150, AllowedLinks: []string{"https://prateeksahni.pages.dev/projects/", "https://www.linkedin.com/posts/prateek-sahni-meng"}}

func TestCheck(t *testing.T) {
	cases := []struct {
		name     string
		comment  string
		existing []string
		want     []string // rules expected to fire; none means the comment passes
	}{
		{"good, specific", "Layer 3 is where my CRM agent needed a confirm step before every write. Without it, one bad tool call edits 200 rows.", nil, nil},
		{"good with project link", "We hit this with Gmail flagging a re-saved CV as a virus. Wrote it up: https://prateeksahni.pages.dev/projects/outreach-agent/", nil, nil},
		{"too long", strings.Repeat("word ", 40), nil, []string{"too long"}},
		{"generic opener", "Great post! Agents need guardrails.", nil, []string{"generic opener"}},
		{"hype", "This is a game-changer for sales teams.", nil, []string{"hype or sales word"}},
		{"hashtag", "Agents need audit logs #AI", nil, []string{"hashtag"}},
		{"em dash", "Agents need audit logs — always.", nil, []string{"em dash"}},
		{"two emoji", "Agents need audit logs 🔥🚀", nil, []string{"emoji"}},
		{"foreign link", "See https://example.com/x for more", nil, []string{"link target"}},
		{"plain http link", "See http://prateeksahni.pages.dev/projects/event-crm/", nil, []string{"link target"}},
		{"two links", "https://prateeksahni.pages.dev/projects/a/ and https://prateeksahni.pages.dev/projects/b/", nil, []string{"links"}},
		{"repeats", "Reasoning needs guardrails and a decision layer for agents", []string{"Where reasoning meets guardrails, agents need a decision layer"}, []string{"repeats another comment"}},
		{"different point on the same post", "Layer 2 breaks first in practice: nobody owns the docs, so the agent answers from stale ones.", []string{"Where reasoning meets guardrails, agents need a decision layer"}, nil},
		{"empty", "   ", nil, []string{"empty"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]bool{}
			for _, v := range Check(c.comment, policy, c.existing) {
				got[v.Rule] = true
			}
			if len(c.want) == 0 && len(got) > 0 {
				t.Fatalf("want a pass, got %v", got)
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("want rule %q to fire, got %v", w, got)
				}
			}
		})
	}
}
