// Package draft writes a short comment for a post from Prateek's real project facts, checks it
// against the hard rules, and asks again (once per remaining try) with the broken rules spelled out.
package draft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hksahni0-ux/reach/internal/llm"
	"github.com/hksahni0-ux/reach/internal/rank"
	"github.com/hksahni0-ux/reach/internal/rules"
)

// Project is the part of the website's portfolio.json a comment may draw on.
type Project struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	OneLiner string `json:"oneLiner"`
	Metric   string `json:"metric"`
	Did      string `json:"did"`
	Outcome  string `json:"outcome"`
}

// LoadPortfolio fetches the projects the website publishes, so drafts only cite real work.
func LoadPortfolio(ctx context.Context, site string) ([]Project, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(site, "/")+"/portfolio.json", nil)
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("portfolio.json: HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("portfolio.json: %w", err)
	}
	return doc.Projects, nil
}

// ProjectURL is the page a comment links to.
func ProjectURL(site, id string) string {
	return strings.TrimRight(site, "/") + "/projects/" + id + "/"
}

// Chatter is the model chain; llm.Client satisfies it, tests use a fake.
type Chatter interface {
	Chat(ctx context.Context, msgs []llm.Message) (answer, model string, err error)
}

// Draft is one comment ready for the approval queue, or the reason there isn't one.
type Draft struct {
	Comment    string            `json:"comment"`
	Model      string            `json:"model"`
	Attempts   int               `json:"attempts"`
	Violations []rules.Violation `json:"violations,omitempty"` // non-empty means it failed every try
}

const system = `You write LinkedIn comments for Prateek Sahni, a UK-based engineer who builds AI agents, CRMs and sales automation and has a background in additive manufacturing.

You ARE Prateek, writing in the first person. The post's author is someone else: never describe their background as Prateek's, and never attribute Prateek's experience to them.

Write ONE comment replying to the post. Rules:
- At most %d characters in total, including any link. A project link is about 50 characters, so with a link keep the words under 95. Shorter is better; one or two sentences.
- Add something specific: a concrete lesson, number or trade-off from one of Prateek's projects below, or a sharp question. Never just agree or praise.
- Write complete, natural sentences, as you'd say them to a colleague. Not telegraphic notes, no project names used as jargon.
- Only mention a project if it genuinely matches what the post is about. If none does, comment from general engineering experience or ask a thoughtful question, without naming a project or linking.
- Only claim experience the project facts support. Never invent numbers, percentages, clients or results: any number you use must appear in the facts below.
- Plain British English, first person, sounds like a person typing. No hashtags, no em dashes, at most one emoji (prefer none).
- Never start with praise ("Great post", "Love this", "Spot on", ...). No buzzwords (leverage, unlock, game-changer, seamless, delve, elevate).
- A link is optional. Only if a project is directly on the post's topic, end with its URL from the list, exactly as given. Otherwise no link.
- Don't repeat what the existing comments already say.

Reply with the comment text only, nothing else.`

// Write drafts a comment for p using the given projects, retrying up to tries times.
func Write(ctx context.Context, c Chatter, p rank.Scored, projects []Project, site string, maxChars, tries int) Draft {
	policy := rules.Policy{MaxChars: maxChars, AllowedLinks: []string{strings.TrimRight(site, "/") + "/"}}
	facts := userPrompt(p, projects, site)
	msgs := []llm.Message{
		{Role: "system", Content: fmt.Sprintf(system, maxChars)},
		{Role: "user", Content: facts},
	}
	var d Draft
	for d.Attempts < tries {
		d.Attempts++
		answer, model, err := c.Chat(ctx, msgs)
		if err != nil {
			d.Violations = []rules.Violation{{Rule: "model", Detail: err.Error()}}
			return d
		}
		d.Comment, d.Model = clean(answer), model
		d.Violations = append(rules.Check(d.Comment, policy, p.TopComments), unsupportedNumbers(d.Comment, facts)...)
		if len(d.Violations) == 0 {
			return d
		}
		// Too long only because of the link: the comment stands on its own without it.
		if bare := strings.TrimSpace(linkPattern.ReplaceAllString(d.Comment, "")); bare != d.Comment && onlyTooLong(d.Violations) {
			if v := append(rules.Check(bare, policy, p.TopComments), unsupportedNumbers(bare, facts)...); len(v) == 0 {
				d.Comment, d.Violations = bare, nil
				return d
			}
		}
		var why []string
		for _, v := range d.Violations {
			why = append(why, "- "+v.String())
		}
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: d.Comment},
			llm.Message{Role: "user", Content: "That breaks these rules:\n" + strings.Join(why, "\n") + "\nRewrite it. Reply with the comment text only."})
	}
	return d
}

func userPrompt(p rank.Scored, projects []Project, site string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "POST by %s (topic: %s):\n%s\n\n", nonEmpty(p.AuthorName, "unknown"), p.Topic, p.Text)
	if len(p.TopComments) > 0 {
		b.WriteString("EXISTING COMMENTS:\n")
		for i, c := range p.TopComments {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	b.WriteString("PRATEEK'S PROJECTS (facts you may use; link = URL to end with, only if directly relevant):\n")
	for _, pr := range projects {
		fmt.Fprintf(&b, "- %s: %s %s Outcome: %s link = %s\n", pr.Title, pr.OneLiner, pr.Metric, pr.Outcome, ProjectURL(site, pr.ID))
	}
	return b.String()
}

var (
	linkPattern   = regexp.MustCompile(`\s*https?://\S+`)
	numberPattern = regexp.MustCompile(`\d[\d,.]*`)
)

// unsupportedNumbers flags any number in the comment that isn't in the post or project facts,
// the commonest way a model invents a result.
func unsupportedNumbers(comment, facts string) []rules.Violation {
	var v []rules.Violation
	for _, n := range numberPattern.FindAllString(linkPattern.ReplaceAllString(comment, ""), -1) {
		n = strings.TrimRight(n, ".,")
		if n != "" && !strings.Contains(facts, n) {
			v = append(v, rules.Violation{Rule: "unsupported number", Detail: fmt.Sprintf("%q isn't in the facts; don't invent figures", n)})
		}
	}
	return v
}

func onlyTooLong(v []rules.Violation) bool {
	return len(v) == 1 && v[0].Rule == "too long"
}

// clean strips quotes and labels models sometimes wrap the answer in.
func clean(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"Comment:", "comment:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
	}
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"') {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// Relevant returns the projects a topic may draw on, falling back to all of them.
func Relevant(all []Project, ids []string) []Project {
	if len(ids) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []Project
	for _, p := range all {
		if want[p.ID] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}
