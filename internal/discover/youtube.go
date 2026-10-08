package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// YouTube finds viral videos through the YouTube Data API with Prateek's own key. (Composio's
// YouTube connection shares Google's quota with every Composio user and is often exhausted.)
// A search costs 100 of the free 10,000 daily units; stats and comments cost 1 each.
type YouTube struct {
	Key  string
	Base string // https://www.googleapis.com/youtube/v3, swappable for tests
	HTTP *http.Client
}

func NewYouTube(key string) *YouTube {
	return &YouTube{Key: key, Base: "https://www.googleapis.com/youtube/v3", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (y *YouTube) get(ctx context.Context, path string, q url.Values, out any) error {
	q.Set("key", y.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.Base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := y.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("YouTube %s: %w", path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		// Strip the key if Google echoes the URL back.
		return fmt.Errorf("YouTube %s: HTTP %d: %.300s", path, res.StatusCode, strings.ReplaceAll(string(raw), y.Key, "***"))
	}
	return json.Unmarshal(raw, out)
}

// Videos returns each topic's most-viewed videos published after since, with their stats
// and top comments, each video once.
func (y *YouTube) Videos(ctx context.Context, topics []Topic, since time.Time, perQuery int) ([]Post, []error) {
	var (
		posts []Post
		errs  []error
		seen  = map[string]bool{}
	)
	for _, t := range topics {
		for _, q := range t.Queries {
			var s struct {
				Items []struct {
					ID struct {
						VideoID string `json:"videoId"`
					} `json:"id"`
				} `json:"items"`
			}
			err := y.get(ctx, "/search", url.Values{
				"part": {"id"}, "type": {"video"}, "q": {q}, "order": {"viewCount"},
				"publishedAfter": {since.UTC().Format(time.RFC3339)}, "maxResults": {strconv.Itoa(perQuery)},
				"relevanceLanguage": {"en"},
			}, &s)
			if err != nil {
				errs = append(errs, fmt.Errorf("query %q: %w", q, err))
				continue
			}
			var ids []string
			for _, it := range s.Items {
				if id := it.ID.VideoID; id != "" && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			found, err := y.details(ctx, ids, t.Name)
			if err != nil {
				errs = append(errs, fmt.Errorf("query %q: %w", q, err))
			}
			posts = append(posts, found...)
		}
	}
	return posts, errs
}

type videoList struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			PublishedAt  string `json:"publishedAt"`
			ChannelTitle string `json:"channelTitle"`
			ChannelID    string `json:"channelId"`
			Title        string `json:"title"`
			Description  string `json:"description"`
		} `json:"snippet"`
		Statistics struct {
			ViewCount    string `json:"viewCount"`
			LikeCount    string `json:"likeCount"`
			CommentCount string `json:"commentCount"`
		} `json:"statistics"`
	} `json:"items"`
}

// ParseVideos turns a videos.list response into Posts.
func ParseVideos(raw json.RawMessage, topic string) ([]Post, error) {
	var v videoList
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parsing YouTube videos: %w", err)
	}
	posts := make([]Post, 0, len(v.Items))
	for _, it := range v.Items {
		published, _ := time.Parse(time.RFC3339, it.Snippet.PublishedAt)
		n := func(s string) int { i, _ := strconv.Atoi(s); return i }
		text := it.Snippet.Title
		if d := strings.TrimSpace(it.Snippet.Description); d != "" {
			text += "\n\n" + d
		}
		posts = append(posts, Post{
			Platform:   "youtube",
			URL:        "https://www.youtube.com/watch?v=" + it.ID,
			Topic:      topic,
			AuthorName: it.Snippet.ChannelTitle,
			AuthorURL:  "https://www.youtube.com/channel/" + it.Snippet.ChannelID,
			Text:       text,
			Views:      n(it.Statistics.ViewCount),
			Likes:      n(it.Statistics.LikeCount),
			Comments:   n(it.Statistics.CommentCount),
			Published:  published,
		})
	}
	return posts, nil
}

func (y *YouTube) details(ctx context.Context, ids []string, topic string) ([]Post, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var raw json.RawMessage
	if err := y.get(ctx, "/videos", url.Values{"part": {"snippet,statistics"}, "id": {strings.Join(ids, ",")}}, &raw); err != nil {
		return nil, err
	}
	return ParseVideos(raw, topic)
}

// TopComments adds the most-liked comments on a video, so a draft doesn't repeat them.
// Videos with comments turned off are left as they are.
func (y *YouTube) TopComments(ctx context.Context, p Post, n int) Post {
	id := strings.TrimPrefix(p.URL, "https://www.youtube.com/watch?v=")
	var r struct {
		Items []struct {
			Snippet struct {
				TopLevelComment struct {
					Snippet struct {
						TextOriginal string `json:"textOriginal"`
					} `json:"snippet"`
				} `json:"topLevelComment"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := y.get(ctx, "/commentThreads", url.Values{"part": {"snippet"}, "videoId": {id}, "order": {"relevance"}, "maxResults": {strconv.Itoa(n)}, "textFormat": {"plainText"}}, &r); err != nil {
		return p
	}
	for _, it := range r.Items {
		if t := strings.TrimSpace(it.Snippet.TopLevelComment.Snippet.TextOriginal); t != "" {
			p.TopComments = append(p.TopComments, t)
		}
	}
	return p
}
