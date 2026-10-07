// Package discover finds recent public posts worth commenting on.
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hksahni0-ux/reach/internal/composio"
)

// Post is one candidate post, whatever platform it came from.
type Post struct {
	Platform        string    `json:"platform"`
	URL             string    `json:"url"`
	Topic           string    `json:"topic"` // the topic query that found it
	AuthorName      string    `json:"author_name"`
	AuthorURL       string    `json:"author_url"`
	AuthorFollowers int       `json:"author_followers"`
	Text            string    `json:"text"` // opening of the post (search results are truncated)
	Likes           int       `json:"likes"`
	Comments        int       `json:"comments"`
	Published       time.Time `json:"published"`
	TopComments     []string  `json:"top_comments,omitempty"` // what others already said, to avoid repeating it
}

// Topic is a search phrase plus the project pages a comment on it may link to.
type Topic struct {
	Name     string   `json:"name"`
	Queries  []string `json:"queries"`
	Projects []string `json:"projects"` // project ids on the website, e.g. "event-crm"
}

const searchSlug = "SCRAPECREATORS_SEARCH_LINKEDIN_POSTS"

// The fields we use from a ScrapeCreators LinkedIn search result.
type searchResult struct {
	Items []struct {
		URL           string `json:"url"`
		Description   string `json:"description"`
		LikeCount     int    `json:"like_count"`
		CommentCount  int    `json:"comment_count"`
		DatePublished string `json:"date_published"`
		Author        struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			Followers int    `json:"followers"`
		} `json:"author"`
		Comments []struct {
			Text string `json:"text"`
		} `json:"comments"`
	} `json:"items"`
}

// ParseLinkedInSearch turns one page of search results into Posts.
func ParseLinkedInSearch(raw json.RawMessage, topic string) ([]Post, error) {
	var r searchResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parsing LinkedIn search results: %w", err)
	}
	posts := make([]Post, 0, len(r.Items))
	for _, it := range r.Items {
		if it.URL == "" {
			continue
		}
		published, _ := time.Parse(time.RFC3339, it.DatePublished) // zero if missing; ranking drops those
		p := Post{
			Platform:        "linkedin",
			URL:             canonicalLinkedInURL(it.URL),
			Topic:           topic,
			AuthorName:      it.Author.Name,
			AuthorURL:       it.Author.URL,
			AuthorFollowers: it.Author.Followers,
			Text:            strings.TrimSpace(it.Description),
			Likes:           it.LikeCount,
			Comments:        it.CommentCount,
			Published:       published,
		}
		for _, c := range it.Comments {
			if t := strings.TrimSpace(c.Text); t != "" {
				p.TopComments = append(p.TopComments, t)
			}
		}
		posts = append(posts, p)
	}
	return posts, nil
}

// canonicalLinkedInURL drops tracking query strings and regional subdomains (uk.linkedin.com…),
// so the same post found through two topics counts once.
func canonicalLinkedInURL(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.Index(u, "linkedin.com/"); i >= 0 {
		u = "https://www." + u[i:]
	}
	return u
}

// LinkedIn searches every query of every topic in parallel (a few at a time) and returns
// the posts found, each URL once. A failed query is reported but doesn't stop the others.
func LinkedIn(ctx context.Context, ex composio.Executor, topics []Topic, window string) ([]Post, []error) {
	type job struct{ topic, query string }
	var jobs []job
	for _, t := range topics {
		for _, q := range t.Queries {
			jobs = append(jobs, job{t.Name, q})
		}
	}

	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		posts []Post
		errs  []error
		wg    sync.WaitGroup
		slots = make(chan struct{}, 4) // at most 4 searches in flight
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			raw, err := ex.Execute(ctx, searchSlug, map[string]any{"query": j.query, "date_posted": window})
			if err == nil {
				var found []Post
				if found, err = ParseLinkedInSearch(raw, j.topic); err == nil {
					mu.Lock()
					for _, p := range found {
						if !seen[p.URL] {
							seen[p.URL] = true
							posts = append(posts, p)
						}
					}
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			errs = append(errs, fmt.Errorf("query %q: %w", j.query, err))
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	return posts, errs
}
