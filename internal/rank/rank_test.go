package rank

import (
	"testing"
	"time"

	"github.com/hksahni0-ux/reach/internal/discover"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func post(url string, likes, comments, followers int, age time.Duration) discover.Post {
	return discover.Post{URL: url, AuthorURL: "https://www.linkedin.com/in/" + url, Likes: likes, Comments: comments, AuthorFollowers: followers, Published: now.Add(-age)}
}

func TestFreshAndClimbingBeatsOldAndBig(t *testing.T) {
	got := Rank([]discover.Post{
		post("old-big", 400, 60, 20000, 20*time.Hour),
		post("fresh", 60, 15, 20000, 1*time.Hour),
	}, Options{Now: now})
	if got[0].URL != "fresh" {
		t.Fatalf("want the fresh post first, got %s (scores %.2f vs %.2f)", got[0].URL, got[0].Score, got[1].Score)
	}
}

func TestDropsOldUndatedAndExcluded(t *testing.T) {
	undated := post("undated", 999, 99, 1, 0)
	undated.Published = time.Time{}
	got := Rank([]discover.Post{
		post("too-old", 999, 99, 1, 30*time.Hour),
		undated,
		post("prateek-sahni-meng", 999, 99, 1, time.Hour),
		post("keep", 1, 0, 1, time.Hour),
	}, Options{Now: now, ExcludeURLs: []string{"prateek-sahni-meng"}})
	if len(got) != 1 || got[0].URL != "keep" {
		t.Fatalf("want only 'keep', got %+v", got)
	}
}

func TestCrowdedCommentSectionIsMarkedDown(t *testing.T) {
	got := Rank([]discover.Post{post("crowded", 100, 600, 1000, time.Hour)}, Options{Now: now})
	if v := got[0].Visibility; v != 0.25 {
		t.Fatalf("visibility = %v, want 0.25 (150 / 600 comments)", v)
	}
}

func TestBiggerAudienceHelpsWithDiminishingReturns(t *testing.T) {
	got := Rank([]discover.Post{
		post("small", 50, 10, 500, time.Hour),
		post("large", 50, 10, 500000, time.Hour),
	}, Options{Now: now})
	if got[0].URL != "large" {
		t.Fatal("same engagement: the larger audience should rank first")
	}
	if got[0].Reach > 3*got[1].Reach {
		t.Fatalf("reach should grow slowly: %.2f vs %.2f", got[0].Reach, got[1].Reach)
	}
}
