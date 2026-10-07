// Package rules checks a drafted comment before it reaches the approval queue.
// These are the hard rules; a second model scores the softer qualities (specificity, relevance).
package rules

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Policy is what a comment must satisfy.
type Policy struct {
	MaxChars     int      // whole comment, link included, so it never hides behind "see more"
	AllowedLinks []string // URL prefixes a comment may link to (only one link per comment)
}

// Violation is one broken rule, in words the queue can show.
type Violation struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

func (v Violation) String() string { return v.Rule + ": " + v.Detail }

// Openers that add nothing; LinkedIn and readers both treat them as noise.
var genericOpeners = []string{
	"great post", "great share", "thanks for sharing", "thank you for sharing", "love this", "so true",
	"well said", "couldn't agree more", "could not agree more", "this is gold", "spot on", "100%",
	"absolutely agree", "insightful post", "great insights", "nice post", "amazing post", "brilliant post",
}

// Words that make a comment read as machine-written or salesy.
var hypeWords = []string{
	"game-changer", "game changer", "revolutionary", "revolutionize", "delve", "unlock", "leverage",
	"synergy", "paradigm", "cutting-edge", "fast-paced", "in today's", "testament to", "elevate",
	"seamless", "supercharge", "next-level", "dm me", "hire me", "check out my", "link in bio",
}

var urlPattern = regexp.MustCompile(`https?://\S+`)

// Check returns every rule the comment breaks; an empty result means it may go to the queue.
// existing holds comments already on the post, so a draft doesn't just repeat one of them.
func Check(comment string, p Policy, existing []string) []Violation {
	var v []Violation
	c := strings.TrimSpace(comment)
	lower := strings.ToLower(c)

	if c == "" {
		return []Violation{{"empty", "the draft is empty"}}
	}
	if n := utf8.RuneCountInString(c); p.MaxChars > 0 && n > p.MaxChars {
		v = append(v, Violation{"too long", fmt.Sprintf("%d characters, limit %d", n, p.MaxChars)})
	}
	for _, o := range genericOpeners {
		if strings.HasPrefix(lower, o) {
			v = append(v, Violation{"generic opener", fmt.Sprintf("starts with %q", o)})
			break
		}
	}
	for _, w := range hypeWords {
		if strings.Contains(lower, w) {
			v = append(v, Violation{"hype or sales word", fmt.Sprintf("contains %q", w)})
		}
	}
	if strings.Contains(c, "#") {
		v = append(v, Violation{"hashtag", "comments don't need hashtags"})
	}
	if strings.ContainsRune(c, '—') {
		v = append(v, Violation{"em dash", "reads as machine-written; use a comma or full stop"})
	}
	if n := countEmoji(c); n > 1 {
		v = append(v, Violation{"emoji", fmt.Sprintf("%d emoji, at most 1", n)})
	}

	links := urlPattern.FindAllString(c, -1)
	if len(links) > 1 {
		v = append(v, Violation{"links", fmt.Sprintf("%d links, at most 1", len(links))})
	}
	for _, l := range links {
		if !allowed(l, p.AllowedLinks) {
			v = append(v, Violation{"link target", fmt.Sprintf("%s isn't an allowed link", l)})
		}
	}

	for _, e := range existing {
		if s := similarity(c, e); s >= 0.5 {
			v = append(v, Violation{"repeats another comment", fmt.Sprintf("%.0f%% the same words as %q", s*100, shorten(e, 60))})
			break
		}
	}
	return v
}

func allowed(link string, prefixes []string) bool {
	u, err := url.Parse(strings.TrimRight(link, ".,;:!?)"))
	if err != nil || u.Scheme != "https" {
		return false
	}
	clean := u.Scheme + "://" + u.Host + u.Path
	for _, p := range prefixes {
		if strings.HasPrefix(clean, p) {
			return true
		}
	}
	return false
}

func countEmoji(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1F300 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF {
			n++
		}
	}
	return n
}

// similarity is the Jaccard overlap of the two texts' words (ignoring short words and links).
func similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(wa)+len(wb)-inter)
}

func words(s string) map[string]bool {
	s = urlPattern.ReplaceAllString(strings.ToLower(s), " ")
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) > 3 {
			out[w] = true
		}
	}
	return out
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
