package draft

import (
	"context"
	"strings"
	"testing"

	"github.com/hksahni0-ux/reach/internal/discover"
	"github.com/hksahni0-ux/reach/internal/llm"
	"github.com/hksahni0-ux/reach/internal/rank"
)

// scripted answers each call with the next line, and records what it was asked.
type scripted struct {
	answers []string
	calls   [][]llm.Message
}

func (s *scripted) Chat(_ context.Context, msgs []llm.Message) (string, string, error) {
	s.calls = append(s.calls, msgs)
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, "test-model", nil
}

func post() rank.Scored {
	return rank.Scored{Post: discover.Post{AuthorName: "Ann", Topic: "CRM", Text: "Most CRMs fail on adoption."}}
}

func opts(tries int) Options {
	return Options{Site: "https://example.dev", MaxChars: 200, Tries: tries,
		OwnPosts: []OwnPost{{URL: "https://www.linkedin.com/posts/me_slug-activity-7513694713044983808-u0Em", About: "CRMs"}}}
}

func TestWriteRetriesWithTheBrokenRules(t *testing.T) {
	c := &scripted{answers: []string{
		"Great post! So true. https://example.dev/",
		`"Adoption rose for us once follow-ups were automatic, not another field to fill in. https://example.dev/"`,
	}}
	d := Write(context.Background(), c, post(), nil, opts(3))
	if len(d.Violations) != 0 || d.Attempts != 2 {
		t.Fatalf("want a clean draft on attempt 2, got %+v", d)
	}
	if strings.HasPrefix(d.Comment, `"`) {
		t.Fatalf("quotes not stripped: %q", d.Comment)
	}
	retry := c.calls[1][len(c.calls[1])-1].Content
	if !strings.Contains(retry, "generic opener") {
		t.Fatalf("retry prompt should name the broken rule, got %q", retry)
	}
}

func TestWriteGivesUpAfterTries(t *testing.T) {
	c := &scripted{answers: []string{"Great post!", "Great post!"}}
	d := Write(context.Background(), c, post(), nil, opts(2))
	if len(d.Violations) == 0 || d.Attempts != 2 {
		t.Fatalf("want violations after 2 attempts, got %+v", d)
	}
}

func TestWriteRejectsLinksOffSite(t *testing.T) {
	c := &scripted{answers: []string{"We saw the same. https://elsewhere.com/x"}}
	d := Write(context.Background(), c, post(), nil, opts(1))
	if len(d.Violations) == 0 {
		t.Fatal("a link off the website should be rejected")
	}
}

func TestRelevant(t *testing.T) {
	all := []Project{{ID: "a"}, {ID: "b"}}
	if got := Relevant(all, []string{"b"}); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("got %+v", got)
	}
	if got := Relevant(all, []string{"missing"}); len(got) != 2 {
		t.Fatal("unknown ids should fall back to every project")
	}
}

func TestWriteRejectsInventedNumbers(t *testing.T) {
	c := &scripted{answers: []string{"Our CRM got 25% higher engagement. https://example.dev/", "Our CRM supported £30k+ in sales. https://example.dev/projects/event-crm/"}}
	projects := []Project{{ID: "event-crm", Title: "CRM", Metric: "Pipeline behind £30k+ in under 6 months"}}
	d := Write(context.Background(), c, post(), projects, opts(2))
	if len(d.Violations) != 0 || d.Attempts != 2 {
		t.Fatalf("want the invented 25%% rejected and the real £30k kept, got %+v", d)
	}
}

func TestWriteRequiresALink(t *testing.T) {
	c := &scripted{answers: []string{"Adoption follows automation.", "Adoption follows automation. https://www.linkedin.com/feed/update/urn:li:activity:7513694713044983808/"}}
	d := Write(context.Background(), c, post(), nil, opts(2))
	if len(d.Violations) != 0 || d.Attempts != 2 {
		t.Fatalf("want the link-less draft rejected and the own-post link accepted, got %+v", d)
	}
}

func TestShortPostURL(t *testing.T) {
	got := ShortPostURL("https://www.linkedin.com/posts/me_a-long-slug-activity-7513694713044983808-u0Em?utm=x")
	if got != "https://www.linkedin.com/feed/update/urn:li:activity:7513694713044983808/" {
		t.Fatalf("got %s", got)
	}
}
