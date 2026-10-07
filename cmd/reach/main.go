// Command reach finds near-viral posts to comment on, drafts short comments for approval,
// and helps post the approved ones. See README.md.
//
//	reach run      [-source scrapecreators|exa] [-drafts 5] [-dry]   find, rank, draft, queue in the Sheet
//	reach assist                                                    copy each approved comment and open its post
//	reach discover [-window last-day] [-top 15] [-out ranked.json] [-from file.json…]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/hksahni0-ux/reach/internal/composio"
	"github.com/hksahni0-ux/reach/internal/discover"
	"github.com/hksahni0-ux/reach/internal/draft"
	"github.com/hksahni0-ux/reach/internal/llm"
	"github.com/hksahni0-ux/reach/internal/queue"
	"github.com/hksahni0-ux/reach/internal/rank"
)

type config struct {
	Site            string           `json:"site"`
	ExcludeAuthors  []string         `json:"exclude_authors"`
	MaxCommentChars int              `json:"max_comment_chars"`
	MinLikes        int              `json:"min_likes"`         // only near-viral posts get a comment
	MinComments     int              `json:"min_comments"`      // and ones where people are talking
	QueriesPerTopic int              `json:"queries_per_topic"` // ScrapeCreators searches cost 1 credit each
	OwnPosts        []draft.OwnPost  `json:"own_posts"`         // Prateek's posts a comment may link to
	Topics          []discover.Topic `json:"topics"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCycle(os.Args[2:])
	case "assist":
		err = runAssist()
	case "discover":
		err = runDiscover(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "reach:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: reach run [-config config/topics.json] [-source scrapecreators|exa] [-drafts 5] [-dry]")
	fmt.Fprintln(os.Stderr, "       reach assist")
	fmt.Fprintln(os.Stderr, "       reach discover [-config config/topics.json] [-window last-day] [-top 15] [-out file.json] [-from saved-search.json …]")
	os.Exit(2)
}

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	cfgPath := fs.String("config", "config/topics.json", "topics and settings")
	window := fs.String("window", "last-day", "how recent: last-hour, last-day or last-week")
	top := fs.Int("top", 15, "how many posts to show")
	out := fs.String("out", "", "also write the ranked posts to this JSON file")
	from := fs.String("from", "", "comma-separated saved search results to rank instead of searching (offline)")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}

	var posts []discover.Post
	if *from != "" {
		for _, f := range strings.Split(*from, ",") {
			raw, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			found, err := discover.ParseLinkedInSearch(raw, "saved")
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			posts = append(posts, found...)
		}
	} else {
		key := os.Getenv("COMPOSIO_API_KEY")
		if key == "" {
			return fmt.Errorf("COMPOSIO_API_KEY is not set (or use -from to rank saved results)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var errs []error
		posts, errs = discover.LinkedIn(ctx, composio.New(key, os.Getenv("COMPOSIO_USER_ID")), cfg.Topics, *window)
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "warning:", e)
		}
		if len(posts) == 0 && len(errs) > 0 {
			return fmt.Errorf("every search failed")
		}
	}

	ranked := rank.Rank(posts, rank.Options{ExcludeURLs: cfg.ExcludeAuthors})
	if len(ranked) > *top {
		ranked = ranked[:*top]
	}
	printTable(ranked)

	if *out != "" {
		b, _ := json.MarshalIndent(ranked, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %d posts to %s\n", len(ranked), *out)
	}
	return nil
}

// runCycle is one scheduled run: find posts from the last day that are already taking off,
// keep only those past the like and comment thresholds, draft a comment with a link for the
// best few, and add them to the approval Sheet. Nothing is posted here.
//
// ScrapeCreators (default) returns likes and comments with each search, so near-viral posts
// can be picked out for 1 credit per phrase. Exa is free but blind to engagement, so with
// -source exa only the newest few are looked up (1 credit each) and filtered.
func runCycle(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "config/topics.json", "topics and settings")
	source := fs.String("source", "scrapecreators", "where to find posts: scrapecreators or exa")
	hours := fs.Int("hours", 24, "only posts from the last this many hours")
	perQuery := fs.Int("per-query", 10, "Exa results per search phrase (-source exa)")
	enrich := fs.Int("enrich", 5, "posts to look up on ScrapeCreators, 1 credit each (-source exa)")
	drafts := fs.Int("drafts", 5, "comments to draft")
	tries := fs.Int("tries", 3, "attempts per draft to pass the comment rules")
	dry := fs.Bool("dry", false, "print the drafts instead of adding them to the Sheet")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	env, err := need("COMPOSIO_API_KEY", "COMPOSIO_USER_ID", "NVIDIA_API_KEY")
	if err != nil {
		return err
	}
	sheetID := os.Getenv("SHEET_ID")
	if sheetID == "" && !*dry {
		return fmt.Errorf("SHEET_ID is not set (or use -dry)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cx := composio.New(env["COMPOSIO_API_KEY"], env["COMPOSIO_USER_ID"])
	sheet := queue.Sheet{Ex: cx, ID: sheetID}

	queued := map[string]bool{}
	if sheetID != "" {
		if queued, err = sheet.QueuedURLs(ctx); err != nil {
			return fmt.Errorf("reading the Sheet: %w", err)
		}
	}

	var found []discover.Post
	var errs []error
	switch *source {
	case "scrapecreators":
		found, errs = discover.LinkedIn(ctx, cx, firstQueries(cfg.Topics, cfg.QueriesPerTopic), "last-day")
	case "exa":
		var stubs []discover.Post
		stubs, errs = discover.ExaLinkedIn(ctx, cx, cfg.Topics, time.Now().Add(-time.Duration(*hours)*time.Hour), *perQuery)
		warn(errs)
		stubs = notQueued(stubs, queued)
		found, errs = discover.Enrich(ctx, cx, stubs, *enrich)
	default:
		return fmt.Errorf("unknown -source %q", *source)
	}
	warn(errs)

	var hot []discover.Post
	for _, p := range notQueued(found, queued) {
		if p.Likes >= cfg.MinLikes && p.Comments >= cfg.MinComments {
			hot = append(hot, p)
		}
	}
	fmt.Fprintf(os.Stderr, "%s: %d posts, %d new with %d+ likes and %d+ comments\n", *source, len(found), len(hot), cfg.MinLikes, cfg.MinComments)
	ranked := rank.Rank(hot, rank.Options{ExcludeURLs: cfg.ExcludeAuthors, MaxAge: time.Duration(*hours) * time.Hour})
	if len(ranked) > *drafts {
		ranked = ranked[:*drafts]
	}
	if len(ranked) == 0 {
		fmt.Fprintln(os.Stderr, "nothing near-viral this run; lower min_likes or min_comments in the config to cast wider")
		return nil
	}
	printTable(ranked)

	projects, err := draft.LoadPortfolio(ctx, cfg.Site)
	if err != nil {
		return fmt.Errorf("loading the website's projects: %w", err)
	}
	models := llm.DefaultModels
	if m := os.Getenv("REACH_MODELS"); m != "" {
		models = strings.Split(m, ",")
	}
	ai := llm.New(env["NVIDIA_API_KEY"], models)
	byTopic := map[string][]string{}
	for _, t := range cfg.Topics {
		byTopic[t.Name] = t.Projects
	}

	var rows [][]any
	for _, p := range ranked {
		d := draft.Write(ctx, ai, p, draft.Relevant(projects, byTopic[p.Topic]),
			draft.Options{Site: cfg.Site, MaxChars: cfg.MaxCommentChars, Tries: *tries, OwnPosts: cfg.OwnPosts})
		status := "ok"
		if len(d.Violations) > 0 {
			status = "needs edit: " + d.Violations[0].String()
		}
		fmt.Printf("\n%s\n  %s\n  [%d chars, %s, %s]\n", p.URL, d.Comment, len([]rune(d.Comment)), d.Model, status)
		rows = append(rows, queue.Row(p, d, time.Now()))
	}
	if *dry {
		return nil
	}
	if err := sheet.Append(ctx, rows); err != nil {
		return fmt.Errorf("adding to the Sheet: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nadded %d drafts to the Sheet\n", len(rows))
	return nil
}

// runAssist is the hands-on way to post when the API can't: for each approved comment it puts
// the text on the clipboard and opens the post, Prateek pastes and clicks Post, then presses
// Enter here and the row is marked posted. macOS only (pbcopy, open).
func runAssist() error {
	env, err := need("COMPOSIO_API_KEY", "COMPOSIO_USER_ID", "SHEET_ID")
	if err != nil {
		return err
	}
	ctx := context.Background()
	sheet := queue.Sheet{Ex: composio.New(env["COMPOSIO_API_KEY"], env["COMPOSIO_USER_ID"]), ID: env["SHEET_ID"]}
	approved, err := sheet.Approved(ctx)
	if err != nil {
		return fmt.Errorf("reading the Sheet: %w", err)
	}
	if len(approved) == 0 {
		fmt.Println("Nothing approved in the Sheet.")
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	for i, a := range approved {
		copyCmd := exec.Command("pbcopy")
		copyCmd.Stdin = strings.NewReader(a.Comment)
		if err := copyCmd.Run(); err != nil {
			return fmt.Errorf("copying to the clipboard: %w", err)
		}
		if err := exec.Command("open", a.URL).Run(); err != nil {
			return fmt.Errorf("opening the post: %w", err)
		}
		fmt.Printf("\n[%d/%d] Comment copied and post opened:\n  %s\n", i+1, len(approved), a.Comment)
		fmt.Print("Paste it (Cmd+V), click Post, then press Enter here. Type s and Enter to skip: ")
		answer, _ := in.ReadString('\n')
		status, at := "posted", time.Now()
		if strings.TrimSpace(strings.ToLower(answer)) == "s" {
			status, at = "skip", time.Time{}
		}
		if err := sheet.Mark(ctx, a.Row, status, at); err != nil {
			fmt.Fprintf(os.Stderr, "row %d: couldn't mark it %s: %v\n", a.Row, status, err)
		}
	}
	return nil
}

// firstQueries keeps each topic's first n search phrases (all of them if n is 0).
func firstQueries(topics []discover.Topic, n int) []discover.Topic {
	if n <= 0 {
		return topics
	}
	out := make([]discover.Topic, len(topics))
	for i, t := range topics {
		out[i] = t
		if len(t.Queries) > n {
			out[i].Queries = t.Queries[:n]
		}
	}
	return out
}

func notQueued(posts []discover.Post, queued map[string]bool) []discover.Post {
	var out []discover.Post
	for _, p := range posts {
		if !queued[p.URL] {
			out = append(out, p)
		}
	}
	return out
}

func need(names ...string) (map[string]string, error) {
	out := map[string]string{}
	var missing []string
	for _, n := range names {
		if out[n] = os.Getenv(n); out[n] == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func warn(errs []error) {
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
}

func loadConfig(path string) (config, error) {
	var c config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func printTable(ranked []rank.Scored) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tSCORE\tAGE\tLIKES\tCMTS\tFOLLOWERS\tTOPIC\tAUTHOR\tOPENING")
	for i, p := range ranked {
		fmt.Fprintf(w, "%d\t%.1f\t%.0fh\t%d\t%d\t%d\t%s\t%s\t%s\n",
			i+1, p.Score, p.AgeHours, p.Likes, p.Comments, p.AuthorFollowers, p.Topic, p.AuthorName, oneLine(p.Text, 60))
	}
	w.Flush()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
