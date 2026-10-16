// Command reach finds near-viral posts to comment on, drafts short comments for approval,
// and helps post the approved ones. See README.md.
//
//	reach run      [-platforms linkedin,youtube] [-drafts 5] [-dry]   find, rank, draft, queue in the Sheet
//	reach post     [-max 10] [-dry]                                  post approved YouTube comments as Prateek
//	reach youtube-login [-port 8765]                                 one-time Google sign-in for posting
//	reach assist                                                    copy each approved comment and open its post
//	reach discover [-window last-day] [-top 15] [-out ranked.json] [-from file.json…]
package main

import (
	"bufio"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	"github.com/hksahni0-ux/reach/internal/post"
	"github.com/hksahni0-ux/reach/internal/queue"
	"github.com/hksahni0-ux/reach/internal/rank"
)

type config struct {
	Site            string           `json:"site"`
	ExcludeAuthors  []string         `json:"exclude_authors"`
	MaxCommentChars int              `json:"max_comment_chars"`
	LinkedIn        platform         `json:"linkedin"`
	YouTube         platform         `json:"youtube"`
	QueriesPerTopic int              `json:"queries_per_topic"` // for -source scrapecreators: 1 credit per phrase
	AutoPostYouTube bool             `json:"auto_post_youtube"` // post YouTube drafts that pass every check in the same run, no approval
	OwnPosts        []draft.OwnPost  `json:"own_posts"`         // Prateek's posts a comment may link to
	Topics          []discover.Topic `json:"topics"`
}

// platform holds one platform's thresholds: only posts past all of them get a comment.
type platform struct {
	MinViews    int `json:"min_views,omitempty"`
	MinLikes    int `json:"min_likes"`
	MinComments int `json:"min_comments"`
	PerQuery    int `json:"per_query"` // search results per phrase
}

func (pl platform) passes(p discover.Post) bool {
	return p.Views >= pl.MinViews && p.Likes >= pl.MinLikes && p.Comments >= pl.MinComments
}

func main() {
	loadDotEnv(".env")
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCycle(os.Args[2:])
	case "post":
		err = runPost(os.Args[2:])
	case "youtube-login":
		err = runYouTubeLogin(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: reach run [-config config/topics.json] [-platforms linkedin,youtube] [-source exa|scrapecreators] [-drafts 5] [-dry]")
	fmt.Fprintln(os.Stderr, "       reach post [-max 10] [-dry]")
	fmt.Fprintln(os.Stderr, "       reach youtube-login [-port 8765]")
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

// runCycle is one scheduled run: find posts and videos from the last day that are already
// taking off, keep those past each platform's thresholds, draft a comment with a link for the
// best few, and add them to the approval Sheet. Nothing is posted here.
//
// Both default sources are free. LinkedIn: Exa finds posts and reads each page's comment count
// (Exa can't see likes, so comments are the signal). YouTube: the Data API with Prateek's key,
// sorted by views. -source scrapecreators uses ScrapeCreators for LinkedIn instead (likes and
// comments, 1 credit per phrase).
func runCycle(args []string) (err error) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "config/topics.json", "topics and settings")
	platforms := fs.String("platforms", "linkedin,youtube", "comma-separated: linkedin, youtube")
	source := fs.String("source", "exa", "LinkedIn source: exa (free) or scrapecreators (credits)")
	hours := fs.Int("hours", 24, "only posts from the last this many hours")
	drafts := fs.Int("drafts", 5, "comments to draft per platform")
	tries := fs.Int("tries", 3, "attempts per draft to pass the comment rules")
	dry := fs.Bool("dry", false, "print the drafts instead of adding them to the Sheet")
	noPost := fs.Bool("no-post", false, "don't auto-post YouTube drafts this run, whatever the config says")
	perTopicFlag := fs.Int("per-topic", 0, "drafts per topic, overriding the Settings tab (0 = use the tab)")
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cx := composio.New(env["COMPOSIO_API_KEY"], env["COMPOSIO_USER_ID"])
	sheet := queue.Sheet{Ex: cx, ID: sheetID}

	// Report progress in the Settings tab, so a click on "Find posts now" visibly leads somewhere.
	started := time.Now().UTC()
	report := func(line string) {
		if *dry || sheetID == "" {
			return
		}
		if e := sheet.SetStatus(ctx, line); e != nil {
			fmt.Fprintln(os.Stderr, "warning: couldn't update the status line:", e)
		}
	}
	var summary string
	var totalFound, skippedOff, modelFailed int
	report(fmt.Sprintf("Running since %s UTC: finding posts… (takes about 5–10 minutes)", started.Format("Jan 2 15:04")))
	defer func() {
		if err != nil {
			report(fmt.Sprintf("Last run %s UTC failed: %s", started.Format("Jan 2 15:04"), oneLine(err.Error(), 200)))
		} else {
			report(fmt.Sprintf("Last run %s UTC: %s", started.Format("Jan 2 15:04"), summary))
		}
	}()

	queued := map[string]bool{}
	if sheetID != "" {
		if queued, err = sheet.QueuedURLs(ctx); err != nil {
			return fmt.Errorf("reading the Sheet: %w", err)
		}
	}
	since := time.Now().Add(-time.Duration(*hours) * time.Hour)

	// The Settings tab, if there is one, picks the topics, platforms and drafts per topic.
	var settings queue.Settings
	haveSettings := false
	if sheetID != "" {
		if settings, haveSettings, err = sheet.ReadSettings(ctx); err != nil {
			return fmt.Errorf("reading the Settings tab: %w", err)
		}
	}
	topicsFor := func(platform string) []discover.Topic {
		if !haveSettings {
			return cfg.Topics
		}
		var out []discover.Topic
		for _, t := range cfg.Topics {
			ts, listed := settings.Topics[t.Name]
			if !listed || (ts.Include && (platform == "linkedin" && ts.LinkedIn || platform == "youtube" && ts.YouTube)) {
				out = append(out, t)
			}
		}
		return out
	}
	perTopic := func(topic string) int {
		if *perTopicFlag > 0 {
			return *perTopicFlag
		}
		if ts := settings.Topics[topic]; ts.Drafts > 0 {
			return ts.Drafts
		}
		return 1
	}

	var ranked []rank.Scored
	for _, name := range strings.Split(*platforms, ",") {
		topics := topicsFor(strings.TrimSpace(name))
		if len(topics) == 0 {
			fmt.Fprintf(os.Stderr, "%s: no topics selected in Settings\n", name)
			continue
		}
		var (
			found []discover.Post
			errs  []error
			pl    platform
		)
		switch strings.TrimSpace(name) {
		case "linkedin":
			pl = cfg.LinkedIn
			if *source == "scrapecreators" {
				found, errs = discover.LinkedIn(ctx, cx, firstQueries(topics, cfg.QueriesPerTopic), "last-day")
			} else {
				var stubs []discover.Post
				stubs, errs = discover.ExaLinkedIn(ctx, cx, topics, since, max(pl.PerQuery, 10))
				warn(errs)
				found, errs = discover.ExaCounts(ctx, cx, notQueued(stubs, queued))
			}
		case "youtube":
			pl = cfg.YouTube
			key := os.Getenv("YOUTUBE_API_KEY")
			if key == "" {
				fmt.Fprintln(os.Stderr, "youtube: skipped, YOUTUBE_API_KEY is not set")
				continue
			}
			found, errs = discover.NewYouTube(key).Videos(ctx, topics, since, max(pl.PerQuery, 5))
		default:
			return fmt.Errorf("unknown platform %q", name)
		}
		warn(errs)
		totalFound += len(found)

		var hot []discover.Post
		for _, p := range notQueued(found, queued) {
			if pl.passes(p) {
				hot = append(hot, p)
			}
		}
		fmt.Fprintf(os.Stderr, "%s: %d found, %d new past %d+ views, %d+ likes, %d+ comments\n",
			name, len(found), len(hot), pl.MinViews, pl.MinLikes, pl.MinComments)
		best := rank.Rank(hot, rank.Options{ExcludeURLs: cfg.ExcludeAuthors, MaxAge: time.Duration(*hours) * time.Hour})
		if haveSettings || *perTopicFlag > 0 {
			best = capPerTopic(best, perTopic)
		} else if len(best) > *drafts {
			best = best[:*drafts]
		}
		if name == "youtube" {
			yt := discover.NewYouTube(os.Getenv("YOUTUBE_API_KEY"))
			for i := range best {
				best[i].Post = yt.TopComments(ctx, best[i].Post, 5)
			}
		}
		ranked = append(ranked, best...)
	}
	// Posts asked for by link in the Settings tab are drafted whatever their numbers or age.
	for _, link := range settings.Posts {
		var (
			p   discover.Post
			err error
		)
		switch {
		case strings.Contains(link, "linkedin.com/"):
			p, err = discover.LinkedInPost(ctx, cx, link, "Requested")
		case strings.Contains(link, "youtube.com/") || strings.Contains(link, "youtu.be/"):
			key := os.Getenv("YOUTUBE_API_KEY")
			if key == "" {
				err = fmt.Errorf("YOUTUBE_API_KEY is not set")
				break
			}
			p, err = discover.NewYouTube(key).Video(ctx, link, "Requested")
		default:
			err = fmt.Errorf("only LinkedIn and YouTube links are supported")
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "requested %s: %v\n", link, err)
			continue
		}
		if queued[p.URL] {
			continue // already drafted on an earlier run
		}
		ranked = append(ranked, rank.Scored{Post: p, AgeHours: time.Since(p.Published).Hours()})
	}
	if len(ranked) == 0 {
		fmt.Fprintln(os.Stderr, "nothing viral enough this run; lower the thresholds in the config to cast wider")
		summary = fmt.Sprintf("found %d posts, none new past the thresholds, so no new drafts", totalFound)
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
	byName := map[string]discover.Topic{}
	for _, t := range cfg.Topics {
		byTopic[t.Name] = t.Projects
		byName[t.Name] = t
	}

	// YouTube comments may be posted in the same run (auto_post_youtube), but only drafts that
	// passed every check; anything flagged still goes to the Sheet for a human.
	var yt *post.YouTube
	if cfg.AutoPostYouTube && !*noPost && !*dry {
		if creds, err := need("YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET", "YOUTUBE_REFRESH_TOKEN"); err == nil {
			yt = post.NewYouTube(creds["YOUTUBE_CLIENT_ID"], creds["YOUTUBE_CLIENT_SECRET"], creds["YOUTUBE_REFRESH_TOKEN"])
		} else {
			fmt.Fprintln(os.Stderr, "warning: auto_post_youtube is on but", err, "- YouTube drafts go to the Sheet instead")
		}
	}
	var autoPosted, autoFailed int

	var rows [][]any
	for _, p := range ranked {
		d := draft.Write(ctx, ai, p, draft.Relevant(projects, byTopic[p.Topic]),
			draft.Options{Platform: p.Platform, Site: cfg.Site, MaxChars: cfg.MaxCommentChars, Tries: *tries, OwnPosts: cfg.OwnPosts,
				Angle: byName[p.Topic].Angle, Links: byName[p.Topic].Links})
		if d.Skipped {
			fmt.Fprintf(os.Stderr, "\n%s\n  skipped: not a good place for this comment (%s)\n", p.URL, oneLine(p.Text, 60))
			skippedOff++
			continue
		}
		if len(d.Violations) > 0 && d.Violations[0].Rule == "model" {
			// Every model failed (overloaded, timed out): leave the post out of the Sheet so the
			// next run can try it again, rather than queueing an empty draft.
			fmt.Fprintf(os.Stderr, "\n%s\n  skipped, no model answered: %s\n", p.URL, oneLine(d.Violations[0].Detail, 160))
			modelFailed++
			continue
		}
		status := "ok"
		if len(d.Violations) > 0 {
			status = "needs edit: " + d.Violations[0].String()
		}
		fmt.Printf("\n%s\n  %s\n  [%d chars, %s, %s]\n", p.URL, d.Comment, len([]rune(d.Comment)), d.Model, status)
		row := queue.Row(p, d, time.Now())
		if yt != nil && p.Platform == "youtube" && len(d.Violations) == 0 && d.Comment != "" {
			if autoPosted > 0 {
				time.Sleep(20 * time.Second) // a little space between comments
			}
			if cid, err := yt.Comment(ctx, discover.VideoID(p.URL), d.Comment); err != nil {
				autoFailed++
				fmt.Fprintf(os.Stderr, "  auto-post failed: %v\n", err)
				row[1] = "failed: " + oneLine(err.Error(), 200)
			} else {
				autoPosted++
				fmt.Printf("  posted on YouTube (comment %s)\n", cid)
				row[1], row[15] = "posted", time.Now().UTC().Format("2006-01-02 15:04")
			}
		}
		if *dry {
			continue
		}
		// Each draft goes in as soon as it's written, so a run that hits its time limit
		// keeps what it finished.
		if err := sheet.Append(ctx, [][]any{row}); err != nil {
			return fmt.Errorf("adding to the Sheet: %w", err)
		}
		rows = append(rows, row)
	}
	if *dry {
		return nil
	}
	fmt.Fprintf(os.Stderr, "\nadded %d drafts to the Sheet\n", len(rows))
	summary = fmt.Sprintf("found %d posts, added %d drafts to Sheet1", totalFound, len(rows))
	if autoPosted > 0 || autoFailed > 0 {
		summary += fmt.Sprintf(" (%d posted on YouTube automatically", autoPosted)
		if autoFailed > 0 {
			summary += fmt.Sprintf(", %d failed", autoFailed)
		}
		summary += ")"
	}
	if skippedOff > 0 {
		summary += fmt.Sprintf(", skipped %d as off-topic", skippedOff)
	}
	if modelFailed > 0 {
		summary += fmt.Sprintf(", %d not drafted (AI busy, retried next run)", modelFailed)
	}
	return nil
}

// runPost posts the approved YouTube comments as Prateek through YouTube's official API, a
// few per run, and marks each row. LinkedIn rows are left for `reach assist` (LinkedIn's
// comment API is partner-only). Each row is marked "posting" first, so a crash can't
// post twice; then "posted" with the time, or "failed: why".
func runPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ExitOnError)
	maxN := fs.Int("max", 10, "most comments to post this run")
	dry := fs.Bool("dry", false, "list what would be posted without posting")
	fs.Parse(args)

	env, err := need("COMPOSIO_API_KEY", "COMPOSIO_USER_ID", "SHEET_ID")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sheet := queue.Sheet{Ex: composio.New(env["COMPOSIO_API_KEY"], env["COMPOSIO_USER_ID"]), ID: env["SHEET_ID"]}
	approved, err := sheet.Approved(ctx)
	if err != nil {
		return fmt.Errorf("reading the Sheet: %w", err)
	}
	var todo []queue.Approved
	for _, a := range approved {
		if a.Platform == "youtube" && len(todo) < *maxN {
			todo = append(todo, a)
		}
	}
	fmt.Fprintf(os.Stderr, "%d approved YouTube comments to post\n", len(todo))
	if *dry || len(todo) == 0 {
		for _, a := range todo {
			fmt.Printf("row %d: %s\n  %s\n", a.Row, a.URL, a.Comment)
		}
		return nil
	}
	yt, err := need("YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET", "YOUTUBE_REFRESH_TOKEN")
	if err != nil {
		return fmt.Errorf("%w (run `reach youtube-login` once)", err)
	}
	client := post.NewYouTube(yt["YOUTUBE_CLIENT_ID"], yt["YOUTUBE_CLIENT_SECRET"], yt["YOUTUBE_REFRESH_TOKEN"])

	failed := 0
	for _, a := range todo {
		id := discover.VideoID(a.URL)
		if id == "" {
			failed++
			sheet.Mark(ctx, a.Row, "failed: no video ID in the link", time.Time{})
			continue
		}
		if err := sheet.Mark(ctx, a.Row, "posting", time.Time{}); err != nil {
			return fmt.Errorf("row %d: marking as posting: %w", a.Row, err) // don't post what we can't record
		}
		cid, err := client.Comment(ctx, id, a.Comment)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "row %d: %v\n", a.Row, err)
			if mErr := sheet.Mark(ctx, a.Row, "failed: "+oneLine(err.Error(), 200), time.Time{}); mErr != nil {
				fmt.Fprintf(os.Stderr, "row %d: also couldn't record the failure: %v\n", a.Row, mErr)
			}
			continue
		}
		fmt.Printf("row %d: posted comment %s on %s\n", a.Row, cid, a.URL)
		if err := sheet.Mark(ctx, a.Row, "posted", time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "row %d: posted, but couldn't mark it (it stays \"posting\", so it won't repeat): %v\n", a.Row, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d YouTube comments failed; see the Sheet", failed, len(todo))
	}
	return nil
}

// runYouTubeLogin signs Prateek in to Google once, with his own Cloud OAuth client, and saves
// the long-lived refresh token to .env for `reach post`. Google sends the browser back to a
// one-off local page, so nothing needs to be copied by hand.
//
// A Desktop OAuth client accepts any local port. A Web client only accepts redirect URIs
// registered on it, so register http://127.0.0.1:<port>/ and pass the same -port.
func runYouTubeLogin(args []string) error {
	fs := flag.NewFlagSet("youtube-login", flag.ExitOnError)
	port := fs.Int("port", 0, "fixed local port, for a Web OAuth client with http://127.0.0.1:<port>/ registered (0 = any, for a Desktop client)")
	fs.Parse(args)
	env, err := need("YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET")
	if err != nil {
		return fmt.Errorf("%w (create a Desktop OAuth client in Google Cloud first)", err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	stateBytes := make([]byte, 16)
	crand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	codes := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state || q.Get("code") == "" {
			http.Error(w, "Sign-in didn't complete: "+q.Get("error"), http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Signed in. You can close this tab and go back to the terminal.")
		codes <- q.Get("code")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	u := post.AuthURL(env["YOUTUBE_CLIENT_ID"], redirect, state)
	fmt.Println("Opening Google sign-in. If the browser doesn't open, visit:\n" + u)
	exec.Command("open", u).Run()

	var code string
	select {
	case code = <-codes:
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("no sign-in within 5 minutes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, err := post.Exchange(ctx, env["YOUTUBE_CLIENT_ID"], env["YOUTUBE_CLIENT_SECRET"], code, redirect)
	if err != nil {
		return err
	}
	if err := setDotEnv(".env", "YOUTUBE_REFRESH_TOKEN", token); err != nil {
		return fmt.Errorf("saving to .env: %w", err)
	}
	fmt.Println("Saved YOUTUBE_REFRESH_TOKEN to .env. Add it as a GitHub secret too, for the daily run.")
	return nil
}

// setDotEnv sets KEY=value in a .env file, replacing the line if it's there.
func setDotEnv(path, key, value string) error {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
			lines[i], found = key+"="+value, true
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
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
	if os.Getenv("YOUTUBE_REFRESH_TOKEN") != "" {
		// YouTube comments are posted automatically by `reach post`; only the rest need hands.
		var manual []queue.Approved
		for _, a := range approved {
			if a.Platform != "youtube" {
				manual = append(manual, a)
			}
		}
		approved = manual
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

// capPerTopic keeps, in ranked order, at most limit(topic) posts per topic.
func capPerTopic(ranked []rank.Scored, limit func(topic string) int) []rank.Scored {
	n := map[string]int{}
	var out []rank.Scored
	for _, p := range ranked {
		if n[p.Topic] < limit(p.Topic) {
			n[p.Topic]++
			out = append(out, p)
		}
	}
	return out
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

// loadDotEnv sets KEY=value lines from a local .env file, without overriding anything already
// set (on GitHub Actions the values come from secrets and there is no file).
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k = strings.TrimSpace(k); os.Getenv(k) == "" {
			os.Setenv(k, strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
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
	fmt.Fprintln(w, "#\tWHERE\tSCORE\tAGE\tVIEWS\tLIKES\tCMTS\tTOPIC\tAUTHOR\tOPENING")
	for i, p := range ranked {
		fmt.Fprintf(w, "%d\t%s\t%.1f\t%.0fh\t%d\t%d\t%d\t%s\t%s\t%s\n",
			i+1, p.Platform, p.Score, p.AgeHours, p.Views, p.Likes, p.Comments, p.Topic, p.AuthorName, oneLine(p.Text, 60))
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
