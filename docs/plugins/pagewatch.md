# Page Watch

Page Watch is a small confidence plugin for the complete host capability pipeline. It checks
one public HTTPS page every six hours or on demand, stores its normalized visible text, reports
content drift, and alerts when configured text disappears.

It is intentionally narrower than BIDRL. The useful output is a current page summary, a
100-check history, browser and AI timings, token usage, cost, and an actionable missing-text
alert. A successful run exercises jobs, policy, browser, AI routing and accounting, blobs,
plugin SQL, transactional events, a durable subscriber, HTTP, SSE, and notifications.

## Setup

1. On the Page Watch plugin screen (or Models), connect a provider and pick a model for
   **A short assessment when the watched page changes** (`cheap-chat`, chat). The fake
   provider is enough for a local pipeline check. The route needs at least 128 input tokens
   and 64 output tokens; assignment picks conservative defaults.
2. Give `pagewatch` a finite daily budget. It is an automated plugin, so the host will not
   enable it without one.
3. Enable Page Watch under Plugins.
4. Set `url` to a public HTTPS page and `expectedText` to text that should remain visible.
   Leave `expectedText` empty to monitor content changes only.
5. Open `/pagewatch` and select **Check now**.

The default target is `https://example.com/` with expected text `Example Domain`. The fake
browser has a canned response for this target. Configure `browser.engine: playwright` to test
real outbound browsing.

## Check Semantics

The job opens the configured page through `Host.Browser`, waits for `body`, removes hidden
markup and tags, collapses whitespace, and hashes the resulting text.

| Status | Meaning |
|---|---|
| `baseline` | First successful check, or the configured URL changed. |
| `unchanged` | The normalized text hash matches the previous check. |
| `changed` | The normalized text hash changed. |
| `attention` | The configured expected text is missing. |

Baseline, changed, and attention checks call the `cheap-chat` route. An unchanged page calls it
only when the previous AI summary is at least 24 hours old. Requests allow 64 output tokens and
the complete prompt is capped at 480 UTF-8 bytes.

Successful checks replace the `latest.txt` blob, atomically update plugin state, and publish
`pagewatch.check.completed`. The durable `pagewatch.checks` subscriber projects that event into
the latest 100 history rows. Attention checks publish `pagewatch.alert` in the same transaction,
which matches the default notification rule.

Browser, AI, storage, or publication failures fail the job rather than creating a false
successful check. The host retries up to three times; exhaustion appears through the existing
dead-job event and inbox flow.

## Plugin Surface

| Surface | Contract |
|---|---|
| Job | `check`, cron `17 */6 * * *` UTC, two-minute timeout, three attempts |
| API | `GET /api/plugins/pagewatch/checks` |
| API | `POST /api/plugins/pagewatch/checks` |
| Events | `pagewatch.check.completed`, `pagewatch.alert` |
| Blob | authenticated download of the latest normalized text |
| UI | `/pagewatch`, plus dashboard tile and plugin detail panel |

The visible-text conversion is deliberately approximate. It provides stable monitoring input;
it is not an HTML sanitizer and its output is served as a text attachment.
