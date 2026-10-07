// Package queue keeps the approval queue in a Google Sheet (through Composio): drafts go in as
// "pending", Prateek changes Status to "approve" or "skip", and only approved rows get posted.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hksahni0-ux/reach/internal/composio"
	"github.com/hksahni0-ux/reach/internal/draft"
	"github.com/hksahni0-ux/reach/internal/rank"
)

const (
	appendSlug = "GOOGLESHEETS_SPREADSHEETS_VALUES_APPEND"
	getSlug    = "GOOGLESHEETS_BATCH_GET"
	urlColumn  = "Sheet1!J2:J" // Post URL, below the header
)

// Header is the Sheet's first row; Row must match it column for column.
var Header = []string{"Added", "Status", "Platform", "Score", "Age (h)", "Comments", "Followers", "Author", "Topic", "Post URL", "Post opening", "Draft comment", "Chars", "Model", "Rule problems", "Posted at"}

// Row lays out one draft in Header order. A draft that failed its rules goes in as "needs edit".
func Row(p rank.Scored, d draft.Draft, now time.Time) []any {
	status := "pending"
	var problems []string
	for _, v := range d.Violations {
		problems = append(problems, v.String())
	}
	if len(problems) > 0 {
		status = "needs edit"
	}
	opening := strings.Join(strings.Fields(p.Text), " ")
	if r := []rune(opening); len(r) > 300 {
		opening = string(r[:300]) + "…"
	}
	return []any{
		now.UTC().Format("2006-01-02 15:04"), status, p.Platform,
		fmt.Sprintf("%.1f", p.Score), fmt.Sprintf("%.0f", p.AgeHours), p.Comments, p.AuthorFollowers,
		p.AuthorName, p.Topic, p.URL, opening, d.Comment, len([]rune(d.Comment)), d.Model,
		strings.Join(problems, "; "), "",
	}
}

// Sheet is the queue in one spreadsheet.
type Sheet struct {
	Ex composio.Executor
	ID string
}

// Append adds rows below the existing ones.
func (s Sheet) Append(ctx context.Context, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := s.Ex.Execute(ctx, appendSlug, map[string]any{
		"spreadsheetId":    s.ID,
		"range":            "Sheet1!A1",
		"valueInputOption": "RAW",
		"insertDataOption": "INSERT_ROWS",
		"values":           rows,
	})
	return err
}

var sheetURL = regexp.MustCompile(`https://www\.linkedin\.com/[^"\\\s]+`)

// QueuedURLs returns every post URL already in the Sheet, so a post is never queued twice.
func (s Sheet) QueuedURLs(ctx context.Context) (map[string]bool, error) {
	raw, err := s.Ex.Execute(ctx, getSlug, map[string]any{"spreadsheet_id": s.ID, "ranges": []string{urlColumn}})
	if err != nil {
		return nil, err
	}
	return urlsIn(raw), nil
}

// urlsIn pulls URLs out of a values response without depending on how Composio nests it.
func urlsIn(raw json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for _, u := range sheetURL.FindAllString(string(raw), -1) {
		out[u] = true
	}
	return out
}
