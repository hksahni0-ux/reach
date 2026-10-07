package discover

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func fixture(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("../../testdata/linkedin_search.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseLinkedInSearch(t *testing.T) {
	posts, err := ParseLinkedInSearch(fixture(t), "AI agents")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 3 {
		t.Fatalf("got %d posts, want 3", len(posts))
	}
	p := posts[0]
	if want := "https://www.linkedin.com/posts/example-author_four-layers-for-ai-agents-activity-7000000000000000001-AAAA"; p.URL != want {
		t.Errorf("URL = %q, want %q (regional subdomain and query string removed)", p.URL, want)
	}
	if p.Likes != 55 || p.Comments != 42 || p.AuthorFollowers != 76899 || p.Topic != "AI agents" {
		t.Errorf("unexpected fields: %+v", p)
	}
	if p.Published.IsZero() || len(p.TopComments) != 1 {
		t.Errorf("published or comments not parsed: %+v", p)
	}
	if !posts[2].Published.IsZero() {
		t.Errorf("an empty date should parse as zero, got %v", posts[2].Published)
	}
}

func TestParseLinkedInSearchBadJSON(t *testing.T) {
	if _, err := ParseLinkedInSearch(json.RawMessage(`{"items": 5}`), "x"); err == nil {
		t.Fatal("want an error for malformed results")
	}
}

// fakeExec returns the fixture for every query except "broken".
type fakeExec struct{ data json.RawMessage }

func (f fakeExec) Execute(_ context.Context, _ string, args map[string]any) (json.RawMessage, error) {
	if args["query"] == "broken" {
		return nil, errors.New("rate limited")
	}
	return f.data, nil
}

func TestLinkedInDedupesAndReportsErrors(t *testing.T) {
	topics := []Topic{
		{Name: "AI agents", Queries: []string{"AI agents", "agentic AI"}},
		{Name: "CRM", Queries: []string{"broken"}},
	}
	posts, errs := LinkedIn(context.Background(), fakeExec{fixture(t)}, topics, "last-day")
	if len(posts) != 3 {
		t.Errorf("got %d posts, want 3: the same 3 came back for two queries", len(posts))
	}
	if len(errs) != 1 {
		t.Errorf("got %d errors, want 1 for the broken query", len(errs))
	}
}
