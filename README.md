# reach

A small Go tool that grows the visibility of [prateeksahni.pages.dev](https://prateeksahni.pages.dev) and my LinkedIn,
without becoming spam: it finds recent, fast-rising posts on my topics, drafts short comments that add something
specific, checks them, and puts them in a Google Sheet. **Nothing is posted until I approve it.**

## Why it works this way

- **Approval before posting.** LinkedIn's terms don't allow bots to comment on their own, and a templated comment
  with a link hurts a reputation more than it helps. The tool does the finding and drafting; I approve each comment.
- **Short comments.** Every comment fits in 150 characters, so it never hides behind "see more".
- **Links rarely, and only when they fit.** At most one link, only to a project page that matches the post's topic
  (or one of my own posts), on roughly one comment in five.
- **Comment early on posts that are climbing.** A fresh post gaining likes quickly beats a big post from yesterday.

## How it ranks posts

```
engagement = likes + 3 × comments                 comments mean discussion, and reach
velocity   = engagement / (age in hours + 2)^1.3  fresh and climbing beats old and big
reach      = 1 + log10(1 + followers / 1000) / 2  a bigger audience helps, with diminishing returns
visibility = min(1, 150 / comments)               a reply among 600 others is rarely seen
score      = velocity × reach × visibility
```

Posts older than 24 hours, posts without a date and my own posts are skipped.

## Comment rules

A draft goes to the queue only if it passes every rule in [`internal/rules`](internal/rules/comment.go):
150 characters or fewer · no generic opener ("Great post!") · no hype or sales words · no hashtags · no em dashes ·
at most one emoji · at most one link, HTTPS, to an allowed page · not a near-copy of a comment already on the post.

## Use

```bash
export COMPOSIO_API_KEY=…            # Composio dashboard → Settings → API keys
export COMPOSIO_USER_ID=…            # the Composio user your LinkedIn account is connected under
go run ./cmd/reach discover          # search every topic, rank, print the top 15
go run ./cmd/reach discover -window last-hour -top 30 -out today.ranked.json
go run ./cmd/reach discover -from testdata/linkedin_search.json   # offline, from saved results
```

Topics, their search phrases, and which project pages each may link to live in [`config/topics.json`](config/topics.json).

## Layout

```
cmd/reach/            the command-line tool
internal/composio/    Composio REST client (standard library only)
internal/discover/    searching LinkedIn through Composio + ScrapeCreators, parsing, de-duplicating
internal/rank/        scoring and ordering candidate posts
internal/rules/       hard rules every drafted comment must pass
config/topics.json    topics, search phrases, linkable projects
testdata/             fixtures (fictional authors)
```

## Roadmap

1. ~~Find and rank posts~~
2. Draft comments with an LLM; a second model scores specificity and relevance; a test set of posts runs in CI
3. Approval queue in Google Sheets; post approved comments, spaced through the day
4. Weekly drafts of my own LinkedIn posts from project pages, scheduled through the official posting API
5. Instagram: my own posts and carousels from project pages (Graph API, own content only)
6. Developer communities: publish write-ups to dev.to automatically (its API allows it, with a canonical link
   to the site); draft Reddit and Hacker News posts for me to submit by hand, since those communities ban
   automated promotion
7. Runs on GitHub Actions, triggered by Apps Script

Tests and checks run on every push (`.github/workflows/ci.yml`): gofmt, `go vet`, `go test -race`, build.
