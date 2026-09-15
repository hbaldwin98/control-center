# Control Center notification Worker

This Worker is a core notification transport. It is deliberately unaware of BIDRL and
of every other plugin: the Control Center notification dispatcher sends it a generic
notification, and the Worker delivers that notification to enrolled Web Push devices.

It uses a Durable Object for the one administrator's browser subscriptions and
`web-push` for VAPID encryption. It can therefore wake an iPhone or Android phone after
the Control Center tab is closed.

## Deploy

From this directory:

```sh
npm install
npx web-push generate-vapid-keys
npx wrangler secret put CC_SHARED_TOKEN
npx wrangler secret put VAPID_PUBLIC_KEY
npx wrangler secret put VAPID_PRIVATE_KEY
npx wrangler secret put VAPID_SUBJECT       # e.g. mailto:admin@example.com
npm run deploy
```

Keep the generated private key and shared token secret. The public VAPID key is returned
by `GET /vapid-public-key`; the other Worker routes require `Authorization: Bearer
<CC_SHARED_TOKEN>`.

## Connect Control Center

1. In **Settings → Credentials**, create an API-key credential whose value is the same
   `CC_SHARED_TOKEN`.
2. In **Settings → Notifications**, add a channel with kind **Cloudflare Worker**.
   Set its endpoint to the deployed Worker URL and its credential id to that API-key
   credential. An enabled channel is attached to the core `plugin-alert` rule.
3. In that channel card, press **Enable phone push** from each browser/device that
   should receive alerts. On iPhone/iPad, add Control Center to the Home Screen first;
   iOS only permits a Home Screen web app to request push permission.
4. Leave the built-in **inbox** channel attached as well. It is the durable fallback if
   the Worker is temporarily unavailable.

The Worker accepts the generic notification contract:

```json
{
  "id": "notification-id",
  "idempotencyKey": "stable-send-row-key",
  "title": "BIDRL saved lot closing in 10 minutes",
  "body": "Lot 42 closes within 10 minutes",
  "url": "/bidrl/lot/42"
}
```

The core sends this only after the event and notification rows are committed. Delivery
is at-least-once; a retry can produce a duplicate push, while expired subscriptions are
removed when the push service returns 404 or 410.

## Email fallback

If Web Push is not available, configure a core **Email (SMTP)** channel instead. The
SMTP password/API token goes in an encrypted API-key credential; the server, sender,
recipient, and TLS mode remain non-secret channel settings. STARTTLS on port 587 is the
default, implicit TLS on 465 is supported, and plaintext SMTP is accepted only for a
localhost relay.
