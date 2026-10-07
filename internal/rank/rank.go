// Package rank orders candidate posts by how much visibility a comment on them is likely to get.
package rank

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hksahni0-ux/reach/internal/discover"
)

// Options tune the ranking. Zero values fall back to the defaults below.
type Options struct {
	Now         time.Time
	MaxAge      time.Duration // ignore posts older than this
	CrowdedAt   int           // past this many comments, a new comment is likely to be buried
	ExcludeURLs []string      // author profile fragments to skip, e.g. your own profile
}

const (
	defaultMaxAge    = 24 * time.Hour
	defaultCrowdedAt = 150
	gravity          = 1.3 // how fast a post's score decays with age (as Hacker News does)
)

// Scored is a post with its score and the parts that made it, so the queue can show why it's there.
type Scored struct {
	discover.Post
	Score      float64 `json:"score"`
	AgeHours   float64 `json:"age_hours"`
	Velocity   float64 `json:"velocity"`   // weighted engagement per hour, decayed by age
	Reach      float64 `json:"reach"`      // audience factor from the author's followers
	Visibility float64 `json:"visibility"` // 1 = room to be seen; lower = crowded comment section
}

// Rank scores and sorts posts, best first, dropping ones that are too old, undated or excluded.
//
//	engagement = likes + 3×comments                (comments mean discussion, and reach)
//	velocity   = engagement / (ageHours + 2)^1.3   (fresh and climbing beats old and big)
//	reach      = 1 + log10(1 + followers/1000) / 2 (bigger audience helps, with diminishing returns)
//	visibility = min(1, crowdedAt / comments)      (a reply among 600 others is rarely seen)
//	score      = velocity × reach × visibility
func Rank(posts []discover.Post, o Options) []Scored {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.MaxAge == 0 {
		o.MaxAge = defaultMaxAge
	}
	if o.CrowdedAt == 0 {
		o.CrowdedAt = defaultCrowdedAt
	}

	out := make([]Scored, 0, len(posts))
	for _, p := range posts {
		if p.Published.IsZero() || excluded(p, o.ExcludeURLs) {
			continue
		}
		age := o.Now.Sub(p.Published)
		if age < 0 {
			age = 0 // clock skew: treat as just posted
		}
		if age > o.MaxAge {
			continue
		}
		h := age.Hours()
		engagement := float64(p.Likes + 3*p.Comments)
		velocity := engagement / math.Pow(h+2, gravity)
		reach := 1 + math.Log10(1+float64(p.AuthorFollowers)/1000)/2
		visibility := 1.0
		if p.Comments > o.CrowdedAt {
			visibility = float64(o.CrowdedAt) / float64(p.Comments)
		}
		out = append(out, Scored{
			Post: p, Score: velocity * reach * visibility,
			AgeHours: h, Velocity: velocity, Reach: reach, Visibility: visibility,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func excluded(p discover.Post, fragments []string) bool {
	for _, f := range fragments {
		if f != "" && strings.Contains(strings.ToLower(p.AuthorURL), strings.ToLower(f)) {
			return true
		}
	}
	return false
}
