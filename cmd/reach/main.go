// Command reach finds high-visibility posts to comment on, drafts short comments for approval,
// and posts the approved ones. See README.md.
//
//	reach discover [-window last-day] [-top 15] [-out ranked.json] [-from file.json…]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/hksahni0-ux/reach/internal/composio"
	"github.com/hksahni0-ux/reach/internal/discover"
	"github.com/hksahni0-ux/reach/internal/rank"
)

type config struct {
	Site            string           `json:"site"`
	ExcludeAuthors  []string         `json:"exclude_authors"`
	MaxCommentChars int              `json:"max_comment_chars"`
	Topics          []discover.Topic `json:"topics"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
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
	fmt.Fprintln(os.Stderr, "usage: reach discover [-config config/topics.json] [-window last-day] [-top 15] [-out file.json] [-from saved-search.json …]")
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
