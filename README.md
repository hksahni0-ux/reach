# reach

A small Go tool that grows the visibility of [prateeksahni.pages.dev](https://prateeksahni.pages.dev) and my LinkedIn,
without becoming spam: it finds recent, fast-rising posts on my topics, drafts short comments that add something
specific and link back to my work, checks them, and puts them in a Google Sheet. **Nothing is posted until I approve it,
and I post each one myself.**

## Why it works this way

- **Approval before posting, and I post.** LinkedIn's terms don't allow bots to comment on their own, and its comment
  API is open only to approved partners. The tool does the finding and drafting; `reach assist` copies each approved
  comment and opens the post, so posting takes a paste and a click.
- **Only near-viral posts.** Under a day old, with 100+ likes and 20+ comments (`min_likes`, `min_comments`), so a
  good comment is seen by many people.
- **Short comments, always with one link.** At most 200 characters including the link. The link is the project page
  that backs the point, else one of my own LinkedIn posts on the subject, else the website.
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

1. **Find.** ScrapeCreators searches one phrase per topic for LinkedIn posts from the last day (1 credit each), with
   likes and comments. (`-source exa` finds posts free through Exa instead, then looks up the newest few at 1 credit
   each; Exa can't see engagement, so it's worse at finding near-viral posts.)
2. **Filter** to posts past the like and comment thresholds that aren't already in the Sheet.
3. **Rank** as above, and keep the best few (`-drafts 5`).
4. **Draft.** Comments are written from the projects the website publishes at `/portfolio.json`, so they only cite
   real work, and end with one link. Models are tried in order on NVIDIA's API until one answers: Nemotron 3 Ultra,
   Llama 3.2 90B, DeepSeek V4.1 Flash, Nemotron 3 Super (`REACH_MODELS` overrides).
5. **Queue.** Drafts go to the Google Sheet as "pending" (or "needs edit" if they failed a rule).
6. **Approve.** I read each draft, edit it if I want, and set Status to "approve" or "skip" from the dropdown.
7. **Post.** `reach assist` copies each approved comment, opens its post, and marks the row "posted" once I've pasted
   it and pressed Enter.

[`.github/workflows/run.yml`](.github/workflows/run.yml) drafts each weekday morning, and can be started by hand or
from Apps Script.

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

[`config/topics.json`](config/topics.json) holds the thresholds, the topics with their search phrases and matching
projects, and `own_posts`: my LinkedIn posts a comment may link to (`{"url": …, "about": "one line"}`).

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
