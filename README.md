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
[`internal/draft`](internal/draft/draft.go) adds one more: every number in a comment must appear in the post or in
the project facts, the commonest way a model invents a result. A draft that breaks a rule is sent back with the
broken rules listed (up to three tries); one that still fails goes to the Sheet as "needs edit".

## How a run works

1. **Find (free).** Exa searches every topic phrase for LinkedIn posts from the last 24 hours. A post's time is read
   from the activity ID in its URL, which is exact.
2. **Look up (1 credit each).** ScrapeCreators fills in the newest few: author, followers, likes, comments and what
   people already said. Credits are limited, so this is capped (`-enrich 5`).
3. **Rank** as below, and keep the best few (`-drafts 5`).
4. **Draft.** Comments are written from the projects the website publishes at `/portfolio.json`, so they only cite
   real work. Models are tried in order on NVIDIA's API until one answers: Nemotron 3 Ultra, Llama 3.2 90B,
   DeepSeek V4.1 Flash, Nemotron 3 Super (`REACH_MODELS` overrides).
5. **Queue.** Drafts go to the Google Sheet as "pending". Posts already in the Sheet are never queued again.

## Use

Keys live in `.env` (git-ignored): `COMPOSIO_API_KEY`, `COMPOSIO_USER_ID`, `NVIDIA_API_KEY`, `SHEET_ID`.

```bash
set -a; . ./.env; set +a
go run ./cmd/reach run -dry          # find, rank and draft; print instead of queueing
go run ./cmd/reach run               # the same, adding drafts to the Sheet
go run ./cmd/reach discover          # ScrapeCreators search only (1 credit per phrase), rank, print the top 15
go run ./cmd/reach discover -window last-hour -top 30 -out today.ranked.json
go run ./cmd/reach discover -from testdata/linkedin_search.json   # offline, from saved results
```

Topics, their search phrases, and which project pages each may link to live in [`config/topics.json`](config/topics.json).

## Layout

```
cmd/reach/            the command-line tool
internal/composio/    Composio REST client (standard library only)
internal/discover/    finding posts (Exa, ScrapeCreators), dating them, de-duplicating
internal/rank/        scoring and ordering candidate posts
internal/rules/       hard rules every drafted comment must pass
internal/llm/         NVIDIA model chain with fallback
internal/draft/       writing comments from real project facts, checking, retrying
internal/queue/       the Google Sheet approval queue
config/topics.json    topics, search phrases, linkable projects
testdata/             fixtures (fictional authors)
```

## Roadmap

1. ~~Find and rank posts~~
2. ~~Draft comments with an LLM~~; next, a second model scores specificity and relevance, and a test set of posts runs in CI
3. ~~Approval queue in Google Sheets~~; next, post approved comments, spaced through the day
4. Weekly drafts of my own LinkedIn posts from project pages, scheduled through the official posting API
5. Instagram: my own posts and carousels from project pages (Graph API, own content only)
6. Developer communities: publish write-ups to dev.to automatically (its API allows it, with a canonical link
   to the site); draft Reddit and Hacker News posts for me to submit by hand, since those communities ban
   automated promotion
7. Runs on GitHub Actions, triggered by Apps Script

Tests and checks run on every push (`.github/workflows/ci.yml`): gofmt, `go vet`, `go test -race`, build.
