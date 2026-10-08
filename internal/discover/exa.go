package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hksahni0-ux/reach/internal/composio"
)

// Exa finds post links for free; ScrapeCreators (1 credit each) then fills in the details
// for the few worth ranking. Exa doesn't return engagement, so it only narrows the field.
const (
	exaSearchSlug = "EXA_SEARCH"
	postSlug      = "SCRAPECREATORS_GET_LINKEDIN_POST"
)

var activityID = regexp.MustCompile(`activity[-:](\d{18,20})`)

// PostedAt reads the posting time from a LinkedIn post URL. The activity ID is a
// snowflake-style ID whose top 41 bits are milliseconds since the Unix epoch.
func PostedAt(url string) (time.Time, bool) {
	m := activityID.FindStringSubmatch(url)
	if m == nil {
		return time.Time{}, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(id >> 22)).UTC(), true
}

type exaResult struct {
	Results []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"results"`
	// Composio sometimes nests the payload one level down.
	ResponseData *struct {
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"results"`
	} `json:"response_data"`
}

// ParseExaSearch turns Exa results into dated LinkedIn post stubs (no engagement yet).
// Results that aren't individual posts, or whose date can't be read, are dropped.
func ParseExaSearch(raw json.RawMessage, topic string) ([]Post, error) {
	var r exaResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parsing Exa results: %w", err)
	}
	items := r.Results
	if len(items) == 0 && r.ResponseData != nil {
		items = r.ResponseData.Results
	}
	var posts []Post
	for _, it := range items {
		if !strings.Contains(it.URL, "linkedin.com/posts/") {
			continue
		}
		published, ok := PostedAt(it.URL)
		if !ok {
			continue
		}
		posts = append(posts, Post{
			Platform:  "linkedin",
			URL:       canonicalLinkedInURL(it.URL),
			Topic:     topic,
			Text:      strings.TrimSpace(it.Title),
			Published: published,
		})
	}
	return posts, nil
}

// ExaLinkedIn searches every query of every topic through Exa for posts published after
// `since`, returning each post once. Like LinkedIn, a failed query doesn't stop the others.
func ExaLinkedIn(ctx context.Context, ex composio.Executor, topics []Topic, since time.Time, perQuery int) ([]Post, []error) {
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
		slots = make(chan struct{}, 4)
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			raw, err := ex.Execute(ctx, exaSearchSlug, map[string]any{
				"query":              j.query,
				"numResults":         perQuery,
				"includeDomains":     []string{"linkedin.com/posts"},
				"startPublishedDate": since.UTC().Format(time.RFC3339),
			})
			if err == nil {
				var found []Post
				if found, err = ParseExaSearch(raw, j.topic); err == nil {
					mu.Lock()
					for _, p := range found {
						// Exa's date filter is loose; the activity ID is exact.
						if p.Published.After(since) && !seen[p.URL] {
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

// postDetail is the part of a ScrapeCreators single-post lookup we use.
type postDetail struct {
	Description   string `json:"description"`
	LikeCount     *int   `json:"like_count"`
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
}

// ApplyPostDetail fills a post stub from a ScrapeCreators single-post lookup.
func ApplyPostDetail(p Post, raw json.RawMessage) (Post, error) {
	var d postDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return p, fmt.Errorf("parsing post details: %w", err)
	}
	if t := strings.TrimSpace(d.Description); t != "" {
		p.Text = t
	}
	if d.LikeCount != nil {
		p.Likes = *d.LikeCount
	}
	p.Comments = d.CommentCount
	p.AuthorName, p.AuthorURL, p.AuthorFollowers = d.Author.Name, d.Author.URL, d.Author.Followers
	if t, err := time.Parse(time.RFC3339, d.DatePublished); err == nil {
		p.Published = t
	}
	p.TopComments = nil
	for _, c := range d.Comments {
		if t := strings.TrimSpace(c.Text); t != "" {
			p.TopComments = append(p.TopComments, t)
		}
	}
	return p, nil
}

// Enrich looks up the `limit` newest posts on ScrapeCreators (1 credit each) and returns
// only those, filled in. Newest first, because Exa gives no engagement to choose by and
// an early comment on a fresh post is the one that gets seen.
func Enrich(ctx context.Context, ex composio.Executor, posts []Post, limit int) ([]Post, []error) {
	sorted := append([]Post(nil), posts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Published.After(sorted[j].Published) })
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	var out []Post
	var errs []error
	for _, p := range sorted {
		raw, err := ex.Execute(ctx, postSlug, map[string]any{"url": p.URL})
		if err == nil {
			if p, err = ApplyPostDetail(p, raw); err == nil {
				out = append(out, p)
				continue
			}
		}
		errs = append(errs, fmt.Errorf("details for %s: %w", p.URL, err))
	}
	return out, errs
}

const exaContentsSlug = "EXA_GET_CONTENTS_ACTION"

// "… | Author Name | 810 comments" at the end of a LinkedIn post page's title.
var titleCounts = regexp.MustCompile(`\|\s*([^|]+?)\s*\|\s*([\d,]+)\s+comments?\s*$`)

type exaContents struct {
	Results []struct {
		URL    string `json:"url"`
		Title  string `json:"title"`
		Author string `json:"author"`
	} `json:"results"`
}

// ApplyExaContents fills author, comment count and opening text from Exa's page contents,
// matched by URL. A post whose page shows no comment count keeps 0 comments.
func ApplyExaContents(posts []Post, raw json.RawMessage) ([]Post, error) {
	var c exaContents
	if err := json.Unmarshal(raw, &c); err != nil {
		return posts, fmt.Errorf("parsing Exa contents: %w", err)
	}
	byURL := map[string]int{}
	for i, p := range posts {
		byURL[p.URL] = i
	}
	for _, r := range c.Results {
		i, ok := byURL[canonicalLinkedInURL(r.URL)]
		if !ok {
			continue
		}
		p := &posts[i]
		if r.Author != "" {
			p.AuthorName = r.Author
		}
		title := strings.TrimSpace(r.Title)
		if m := titleCounts.FindStringSubmatch(title); m != nil {
			if p.AuthorName == "" {
				p.AuthorName = m[1]
			}
			p.Comments, _ = strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
			title = strings.TrimSpace(title[:len(title)-len(m[0])])
		}
		if title != "" {
			p.Text = title
		}
	}
	return posts, nil
}

// ExaCounts reads every post's page through Exa (about $0.001 each, no ScrapeCreators credits)
// to get its comment count and author. Exa can't see likes, so comments are the virality signal.
func ExaCounts(ctx context.Context, ex composio.Executor, posts []Post) ([]Post, []error) {
	var errs []error
	for start := 0; start < len(posts); start += 25 {
		end := min(start+25, len(posts))
		ids := make([]string, 0, end-start)
		for _, p := range posts[start:end] {
			ids = append(ids, p.URL)
		}
		raw, err := ex.Execute(ctx, exaContentsSlug, map[string]any{"ids": ids, "text": false})
		if err == nil {
			_, err = ApplyExaContents(posts[start:end], raw)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("Exa contents: %w", err))
		}
	}
	return posts, errs
}
