# Exposing Control Center to the internet

The defaults assume the opposite: `127.0.0.1:8080`, plain HTTP, and a first-run flow that
only a peer on this machine can complete. `config.Validate` enforces the important half of
that — bind anything other than loopback and TLS becomes mandatory — but the rest is the
operator's to get right.

This is what changes once the listener is reachable from somewhere you do not control.

---

## The short version

```yaml
server:
  addr: 127.0.0.1:8080     # keep the listener local; the proxy is the front door
  trustedProxy: true       # only if that proxy is the *only* way in
  origins:
    - https://cc.example.com
session:
  idle: 1h                 # must be shorter than absolute to mean anything
  absolute: 12h
```

Put a proxy in front that holds a real certificate, and never publish the app's own port.

---

## Certificates

The container generates a self-signed `CN=localhost` certificate on first start so
`docker compose up` works with no arguments. That is right for a loopback deployment and
wrong for an exposed one: every visit warns, and an operator who clicks through a warning
daily has trained away the only signal that would show an active interception.

Terminate TLS in something that does ACME — Caddy and nginx both do — and give this server
either the real certificate or a loopback socket behind that proxy.

`Strict-Transport-Security` is sent on any request that arrived over HTTPS, which includes
a plain hop from a trusted proxy that set `X-Forwarded-Proto: https`. It is deliberately
not sent over plain HTTP: browsers ignore it there, and a loopback development server
should not make promises about a scheme it does not speak.

## Reverse proxies and `trustedProxy`

A proxy on the same host makes every request arrive from `127.0.0.1`. Two privileges hang
off being loopback, and both are wrong to grant to the whole internet:

- **First-run bootstrap.** `POST /api/auth/bootstrap` is offered to local peers only. It
  needs the one-time token printed to the log and closes for good once an administrator
  exists, so the window is narrow — but a non-container deployment restarted before its
  first login can be raced, and `GET /api/auth/status` reports `bootstrapRequired` to
  anyone who asks.
- **Exemption from the shared attempt ceiling**, which exists so a flood from outside can
  never shut out whoever is sitting at the console.

Setting `server.trustedProxy` (or `CC_TRUSTED_PROXY=1`) revokes both — behind a proxy no
request counts as local — and switches the rate-limiting identity from the connection to
`X-Forwarded-For`.

Set it **only** when that proxy is the sole route to the listener. The header is otherwise
attacker-controlled, and believing it would let anyone pick their own rate-limit bucket,
which is worse than having no per-peer limiting at all. The rightmost entry is used, since
that is the one your own proxy appended; everything left of it is hearsay from further
out. Bind the app to loopback so the proxy really is the only way in.

## Rate limiting

Unauthenticated auth endpoints are limited per endpoint **and per peer** (10/minute), with
a looser shared ceiling behind it (100 newly-seen peers/minute) for the distributed flood a
per-peer counter cannot see.

The shared ceiling is charged per newly-seen peer rather than per request, so one address
hammering the endpoint costs everybody else nothing. This matters more than it sounds:
a single shared counter is a lockout weapon, and so is a shared counter one attacker can
drain. Loopback is exempt from the shared ceiling only, and is held to the per-peer one
like anything else.

Password verification is separately capped at one at a time. Argon2id is 64 MiB per call
and these endpoints need no session, so concurrent requests are an out-of-memory kill long
before any per-minute ceiling is reached.

## Sessions

`session.idle` must be shorter than `session.absolute` or it can never close first, and an
abandoned session lasts exactly as long as a used one. The defaults are 1h idle inside a
12h absolute lifetime. An authenticated SSE stream counts as activity and refreshes the
idle timestamp while it remains connected; a backgrounded or disconnected browser still
obeys the idle limit. Shorten both if the browser holding the cookie is not one you
physically control.

A password change signs out every other session, and is the fastest revocation available.

## Watching for attacks

Rejected attempts publish `core.auth.failed`; attempts refused by the limiter publish
`core.auth.locked_out`. Both carry the endpoint and the peer address, and neither carries
any part of the submitted password. Add a notification rule on `core.auth.**` before
exposing the server — without one there is no way to tell being brute-forced from having
forgotten your own password.

## What an authenticated session can do

There is one administrator and no privilege levels, so a stolen session cookie is close to
total. Two gates sit above the session:

- **Credential changes** require a password reauthentication within `session.reauthWindow`
  (5 minutes).
- **Starting a harness session** requires the same, because it runs a configured program
  on the host. Stopping one does not: the safe direction stays easy.

Harness profiles are the sharpest edge in the application. The executable, arguments, and
workspace root come from configuration and a request may only select among them, but
anything you list there is something an authenticated request can run. List as little as
you can live with.

## Health checks

`GET /healthz` is unauthenticated and reports `{"status":"ok"}` and nothing else — no
version, no build, no state. It exists so a proxy or uptime monitor does not need a
session, and so exposing one does not mean exposing anything else.

## Backups

`/data` holds the master key, the SQLite database, and the blob store. Losing the master
key means every stored credential is unrecoverable ciphertext. Nothing in `scripts/`
covers backup or restore yet.
