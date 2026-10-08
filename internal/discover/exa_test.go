package discover

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPostedAtDecodesActivityID(t *testing.T) {
	// Checked against ScrapeCreators' date_published for the same post: 2026-10-05T22:30:20.600Z.
	got, ok := PostedAt("https://www.linkedin.com/posts/someone_slug-activity-7513002666785099776-8hrp")
	want := time.Date(2026, 10, 5, 22, 30, 20, 601e6, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("PostedAt = %v, %v; want %v", got, ok, want)
	}
	if _, ok := PostedAt("https://www.linkedin.com/pulse/an-article"); ok {
		t.Fatal("an article URL has no activity ID")
	}
}

func TestParseExaSearchKeepsOnlyDatedPosts(t *testing.T) {
	raw := json.RawMessage(`{"results":[
		{"url":"https://uk.linkedin.com/posts/a_x-activity-7513002666785099776-8hrp?utm=1","title":"A post"},
		{"url":"https://www.linkedin.com/learning/some-course","title":"A course"},
		{"url":"https://www.linkedin.com/posts/b_no-id","title":"No ID"}]}`)
	posts, err := ParseExaSearch(raw, "AI agents")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	p := posts[0]
	if p.URL != "https://www.linkedin.com/posts/a_x-activity-7513002666785099776-8hrp" || p.Topic != "AI agents" || p.Published.IsZero() {
		t.Fatalf("unexpected post %+v", p)
	}
}

func TestParseExaSearchNestedPayload(t *testing.T) {
	raw := json.RawMessage(`{"response_data":{"results":[{"url":"https://www.linkedin.com/posts/a_x-activity-7513002666785099776-8hrp"}]}}`)
	posts, _ := ParseExaSearch(raw, "t")
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
}

func TestApplyPostDetail(t *testing.T) {
	stub := Post{URL: "u", Topic: "t", Text: "title only"}
	raw := json.RawMessage(`{"description":"Full text","like_count":null,"comment_count":4,
		"date_published":"2026-10-05T22:30:20.600Z","author":{"name":"Ann","url":"https://www.linkedin.com/in/ann","followers":8417},
		"comments":[{"text":" first "},{"text":""}]}`)
	p, err := ApplyPostDetail(stub, raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "Full text" || p.Comments != 4 || p.Likes != 0 || p.AuthorFollowers != 8417 || len(p.TopComments) != 1 || p.TopComments[0] != "first" {
		t.Fatalf("unexpected post %+v", p)
	}
}

func TestApplyExaContents(t *testing.T) {
	posts := []Post{{URL: "https://www.linkedin.com/posts/jw_slug-activity-7513565885429694465-DDfX"}, {URL: "https://www.linkedin.com/posts/b_x-activity-7513565885429694466"}}
	raw := json.RawMessage(`{"results":[
		{"url":"https://www.linkedin.com/posts/jw_slug-activity-7513565885429694465-DDfX","author":"Justin Welsh",
		 "title":"When I started my business, everything was my job.\n- Writing… | Justin Welsh | 1,810 comments"},
		{"url":"https://www.linkedin.com/posts/b_x-activity-7513565885429694466","title":"Bob's Post"}]}`)
	got, err := ApplyExaContents(posts, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Comments != 1810 || got[0].AuthorName != "Justin Welsh" || got[0].Text != "When I started my business, everything was my job.\n- Writing…" {
		t.Fatalf("got %+v", got[0])
	}
	if got[1].Comments != 0 || got[1].Text != "Bob's Post" {
		t.Fatalf("got %+v", got[1])
	}
}
