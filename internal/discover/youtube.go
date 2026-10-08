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
	"unicode"
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
			// Language codes such as "en", "en-GB" or "hi"; often missing.
			DefaultLanguage      string `json:"defaultLanguage"`
			DefaultAudioLanguage string `json:"defaultAudioLanguage"`
		} `json:"snippet"`
		Statistics struct {
			ViewCount    string `json:"viewCount"`
			LikeCount    string `json:"likeCount"`
			CommentCount string `json:"commentCount"`
		} `json:"statistics"`
	} `json:"items"`
}

// ParseVideos turns a videos.list response into Posts, keeping English videos only (see English).
func ParseVideos(raw json.RawMessage, topic string) ([]Post, error) {
	var v videoList
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parsing YouTube videos: %w", err)
	}
	posts := make([]Post, 0, len(v.Items))
	for _, it := range v.Items {
		if !English(it.Snippet.DefaultAudioLanguage, it.Snippet.DefaultLanguage, it.Snippet.Title, it.Snippet.Description) {
			continue
		}
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

// Common English words that rarely appear in other languages' titles. ("a", "no" and "me"
// are left out: they are words in Spanish too.)
var englishWords = map[string]bool{
	"the": true, "and": true, "is": true, "are": true, "to": true, "of": true, "for": true, "with": true,
	"this": true, "that": true, "these": true, "how": true, "what": true, "why": true, "when": true,
	"you": true, "your": true, "it": true, "its": true, "my": true, "we": true, "our": true, "they": true,
	"can": true, "will": true, "from": true, "about": true, "just": true, "have": true, "has": true,
	"was": true, "be": true, "in": true, "at": true, "by": true, "an": true, "or": true, "do": true,
	"does": true, "not": true, "more": true, "using": true, "now": true, "get": true, "into": true,
	"every": true, "should": true, "need": true, "best": true, "new": true,
}

// English reports whether a video is in English, which is the only language Reach comments
// in. A non-English language code from YouTube (audio first) rules a video out. Uploaders
// often mislabel videos as English, though, so the title and description must also be in
// Latin script, and, unless tagged English, use common English words.
// relevanceLanguage=en on the search only nudges results, so this does the filtering.
func English(audioLang, lang, title, description string) bool {
	tagged := false
	for _, code := range []string{audioLang, lang} {
		if code = strings.ToLower(strings.TrimSpace(code)); code != "" {
			if code != "en" && !strings.HasPrefix(code, "en-") {
				return false
			}
			tagged = true
			break
		}
	}
	text := title + " " + description
	if len(text) > 600 {
		text = text[:600]
	}
	letters, latin := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if r < 0x80 {
				latin++
			}
		}
	}
	if letters == 0 || latin*100 < letters*95 { // other scripts, or accented languages (Polish, Spanish, ...)
		return false
	}
	if tagged {
		return true
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' })
	hits := 0
	for _, w := range words {
		if englishWords[w] {
			hits++
		}
	}
	return hits >= 2 || (hits == 1 && len(words) <= 8)
}
