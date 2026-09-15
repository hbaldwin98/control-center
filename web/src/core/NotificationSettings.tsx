import { useCallback, useState, type FormEvent } from "react";
import {
  ApiError,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  Disclosure,
  Field,
  Hint,
  Input,
  Row,
  Select,
  Stack,
  Textarea,
  api,
  useSnapshot,
  type UseSnapshotResult,
} from "@cc/ui";
import type { CatalogEvent, EventCatalog } from "./types";
import { CloudflarePushSetup } from "./PushSetup";

type Rule = {
  id: string;
  enabled: boolean;
  match: string;
  where: string;
  channels: string[];
  title: string;
  body: string;
  url: string;
  throttleSeconds: number;
};

type Channel = {
  id: string;
  kind: string;
  credentialId: string;
  enabled: boolean;
  settings: Record<string, string>;
};

type Health = {
  channelId: string;
  state: string;
  lastError: string;
  lastAttemptAt: string | null;
};

export function NotificationSettings() {
  const rules = useSnapshot<Rule[]>(
    useCallback(
      (signal) =>
        api.snapshot<Rule[]>("/api/admin/notifications/rules", { signal }),
      [],
    ),
    { events: "core.notification.config_changed" },
  );
  const channels = useSnapshot<Channel[]>(
    useCallback(
      (signal) =>
        api.snapshot<Channel[]>("/api/admin/notifications/channels", {
          signal,
        }),
      [],
    ),
    { events: "core.notification.config_changed" },
  );
  const health = useSnapshot<Health[]>(
    useCallback(
      (signal) =>
        api.snapshot<Health[]>("/api/admin/notifications/health", { signal }),
      [],
    ),
    { events: "core.notification.delivery_changed" },
  );

  return (
    <Stack>
      <div className="cc-group__title">Notification channels</div>
      <Hint>
        Secrets live in credentials. A channel stores a credential id, never the
        token. New external channels are added to the plugin-alert rule so
        plugin alerts reach them. Cloudflare channels can enroll this browser
        for native phone push.
      </Hint>
      <ChannelForm
        onChanged={() => {
          channels.reload();
          rules.reload();
        }}
      />
      <Async state={channels} loading="Loading channels…" empty="No channels.">
        {(list) => (
          <Stack>
            {list.map((c) => (
              <ChannelCard
                key={c.id}
                channel={c}
                health={
                  health.status === "ready"
                    ? health.data.find((h) => h.channelId === c.id)
                    : undefined
                }
                onChanged={() => {
                  channels.reload();
                  health.reload();
                  rules.reload();
                }}
              />
            ))}
          </Stack>
        )}
      </Async>

      <div className="cc-group__title">Notification rules</div>
      <RuleForm onChanged={rules.reload} />
      <Async state={rules} loading="Loading rules…" empty="No rules.">
        {(list) => (
          <Stack>
            {list.map((r) => (
              <RuleCard key={r.id} rule={r} onChanged={rules.reload} />
            ))}
          </Stack>
        )}
      </Async>
    </Stack>
  );
}

function formatErr(err: unknown): string {
  return err instanceof ApiError
    ? err.message
    : err instanceof Error
      ? err.message
      : String(err);
}

function ChannelForm({
  initial,
  onChanged,
  onCancel,
}: {
  initial?: Channel | undefined;
  onChanged: () => void;
  onCancel?: (() => void) | undefined;
}) {
  const editing = initial !== undefined;
  const [id, setId] = useState(initial?.id ?? "");
  const [kind, setKind] = useState(initial?.kind ?? "ntfy");
  const [topic, setTopic] = useState(initial?.settings?.topic ?? "");
  const [server, setServer] = useState(initial?.settings?.server ?? "");
  const [endpoint, setEndpoint] = useState(initial?.settings?.endpoint ?? "");
  const [emailFrom, setEmailFrom] = useState(initial?.settings?.from ?? "");
  const [emailTo, setEmailTo] = useState(initial?.settings?.to ?? "");
  const [emailUsername, setEmailUsername] = useState(
    initial?.settings?.username ?? "",
  );
  const [emailSecurity, setEmailSecurity] = useState(
    initial?.settings?.security ?? "starttls",
  );
  const [credentialId, setCredentialId] = useState(initial?.credentialId ?? "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    const settings: Record<string, string> = {};
    if (kind === "ntfy") {
      settings.topic = topic;
      if (server) settings.server = server;
    }
    if (kind === "webpush" || kind === "cloudflare")
      settings.endpoint = endpoint;
    if (kind === "email") {
      settings.server = server;
      settings.from = emailFrom;
      settings.to = emailTo;
      if (emailUsername) settings.username = emailUsername;
      settings.security = emailSecurity;
    }
    void api
      .put(`/api/admin/notifications/channels/${encodeURIComponent(id)}`, {
        kind,
        enabled,
        credentialId,
        settings,
      })
      .then(() => {
        if (!editing) {
          setId("");
          setTopic("");
          setServer("");
          setEndpoint("");
          setEmailFrom("");
          setEmailTo("");
          setEmailUsername("");
          setEmailSecurity("starttls");
          setCredentialId("");
        }
        onChanged();
      })
      .catch((err) => setError(formatErr(err)));
  };

  return (
    <Card title={editing ? `Edit channel ${initial.id}` : "Add channel"}>
      <form onSubmit={submit}>
        <Stack>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          <Field label="Id">
            <Input
              value={id}
              onChange={(e) => setId(e.target.value)}
              required
              pattern="[a-z][a-z0-9_.]*"
              disabled={editing}
            />
          </Field>
          <Field label="Kind">
            <Select value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="ntfy">ntfy</option>
              <option value="webpush">webpush</option>
              <option value="cloudflare">Cloudflare Worker</option>
              <option value="email">Email (SMTP)</option>
            </Select>
          </Field>
          {kind === "ntfy" ? (
            <>
              <Field label="Topic">
                <Input
                  value={topic}
                  onChange={(e) => setTopic(e.target.value)}
                  required
                />
              </Field>
              <Field label="Server" hint="Defaults to https://ntfy.sh">
                <Input
                  value={server}
                  onChange={(e) => setServer(e.target.value)}
                />
              </Field>
            </>
          ) : kind === "email" ? (
            <>
              <Field label="SMTP server" hint="host:port; port defaults to 587">
                <Input
                  value={server}
                  onChange={(e) => setServer(e.target.value)}
                  required
                />
              </Field>
              <Field label="From">
                <Input
                  type="email"
                  value={emailFrom}
                  onChange={(e) => setEmailFrom(e.target.value)}
                  required
                />
              </Field>
              <Field label="To" hint="One or more comma-separated addresses">
                <Input
                  value={emailTo}
                  onChange={(e) => setEmailTo(e.target.value)}
                  required
                />
              </Field>
              <Field
                label="SMTP username"
                hint="Leave blank for a local unauthenticated relay"
              >
                <Input
                  value={emailUsername}
                  onChange={(e) => setEmailUsername(e.target.value)}
                />
              </Field>
              <Field label="Security">
                <Select
                  value={emailSecurity}
                  onChange={(e) => setEmailSecurity(e.target.value)}
                >
                  <option value="starttls">STARTTLS (587)</option>
                  <option value="tls">Implicit TLS (465)</option>
                  <option value="plain">Plain (localhost only)</option>
                </Select>
              </Field>
            </>
          ) : (
            <Field
              label={
                kind === "cloudflare" ? "Worker endpoint" : "Push endpoint"
              }
              hint={
                kind === "cloudflare"
                  ? "The root URL of the Control Center notification Worker"
                  : undefined
              }
            >
              <Input
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                required
              />
            </Field>
          )}
          <Field
            label="Credential id"
            hint={
              kind === "cloudflare"
                ? "An API-key credential containing the Worker shared token."
                : kind === "email" && emailUsername
                  ? "An API-key credential containing the SMTP password or token."
                  : "Optional API key for the channel."
            }
          >
            <Input
              value={credentialId}
              onChange={(e) => setCredentialId(e.target.value)}
              required={
                kind === "cloudflare" ||
                (kind === "email" && emailUsername.trim() !== "")
              }
            />
          </Field>
          <Checkbox
            label="Enabled"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <Row>
            <Button type="submit">Save channel</Button>
            {onCancel ? (
              <Button type="button" onClick={onCancel}>
                Cancel
              </Button>
            ) : null}
          </Row>
        </Stack>
      </form>
    </Card>
  );
}

function ChannelCard({
  channel,
  health,
  onChanged,
}: {
  channel: Channel;
  health?: Health | undefined;
  onChanged: () => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  if (editing) {
    return (
      <ChannelForm
        initial={channel}
        onChanged={() => {
          setEditing(false);
          onChanged();
        }}
        onCancel={() => setEditing(false)}
      />
    );
  }

  return (
    <Card
      title={channel.id}
      actions={
        <>
          <Badge>{channel.kind}</Badge>
          <Badge tone={channel.enabled ? "ok" : "warn"}>
            {channel.enabled ? "on" : "off"}
          </Badge>
          {health ? (
            <Badge tone={health.state === "failed" ? "danger" : "ok"}>
              {health.state}
            </Badge>
          ) : null}
        </>
      }
    >
      <Stack>
        {channel.credentialId ? (
          <Hint>Credential {channel.credentialId}</Hint>
        ) : null}
        {health?.lastError ? (
          <Callout tone="danger">{health.lastError}</Callout>
        ) : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {channel.kind === "cloudflare" && channel.enabled ? (
          <CloudflarePushSetup channelId={channel.id} />
        ) : null}
        {channel.id === "inbox" ? (
          <Hint>Built-in inbox channel.</Hint>
        ) : (
          <Row>
            <Button size="sm" onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button
              size="sm"
              onClick={() => {
                void api
                  .del(
                    `/api/admin/notifications/channels/${encodeURIComponent(channel.id)}`,
                  )
                  .then(onChanged)
                  .catch((err) => setError(formatErr(err)));
              }}
            >
              Delete
            </Button>
          </Row>
        )}
      </Stack>
    </Card>
  );
}

function RuleForm({
  initial,
  onChanged,
  onCancel,
}: {
  initial?: Rule | undefined;
  onChanged: () => void;
  onCancel?: (() => void) | undefined;
}) {
  const editing = initial !== undefined;
  const [id, setId] = useState(initial?.id ?? "");
  const [match, setMatch] = useState(initial?.match ?? "*.alert");
  const [where, setWhere] = useState(initial?.where ?? "");
  const [title, setTitle] = useState(initial?.title ?? "{event.type}");
  const [body, setBody] = useState(initial?.body ?? "{event.subject}");
  const [url, setUrl] = useState(initial?.url ?? "");
  const [channels, setChannels] = useState(
    initial ? initial.channels.join(", ") : "inbox",
  );
  const [throttle, setThrottle] = useState(
    String(initial?.throttleSeconds ?? 0),
  );
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [error, setError] = useState<string | null>(null);
  const catalog = useSnapshot<EventCatalog>(
    useCallback(
      (signal) =>
        api.snapshot<EventCatalog>("/api/admin/notifications/catalog", {
          signal,
        }),
      [],
    ),
  );

  const insertPath = (path: string) => {
    setBody((cur) => {
      const token = `{${path}}`;
      if (!cur.trim()) return token;
      if (cur.includes(token)) return cur;
      return `${cur} ${token}`;
    });
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    void api
      .put(`/api/admin/notifications/rules/${encodeURIComponent(id)}`, {
        enabled,
        match,
        where,
        channels: channels
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        title,
        body,
        url,
        throttleSeconds: Number(throttle) || 0,
      })
      .then(() => {
        if (!editing) setId("");
        onChanged();
      })
      .catch((err) => setError(formatErr(err)));
  };

  return (
    <Stack>
      {editing ? null : (
        <CatalogCard
          catalog={catalog}
          onUseMatch={setMatch}
          onInsertPath={insertPath}
        />
      )}
      <Card title={editing ? `Edit rule ${initial.id}` : "Add rule"}>
        <form onSubmit={submit}>
          <Stack>
            {error ? <Callout tone="danger">{error}</Callout> : null}
            <Field label="Id">
              <Input
                value={id}
                onChange={(e) => setId(e.target.value)}
                required
                disabled={editing}
              />
            </Field>
            <Field label="Match">
              <Input
                value={match}
                onChange={(e) => setMatch(e.target.value)}
                required
              />
            </Field>
            <Field label="Where" hint="Optional filter expression.">
              <Input value={where} onChange={(e) => setWhere(e.target.value)} />
            </Field>
            <Field label="Title">
              <Input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                required
              />
            </Field>
            <Field label="Body">
              <Textarea
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
            </Field>
            <Field label="Url" hint="Optional application-relative path.">
              <Input value={url} onChange={(e) => setUrl(e.target.value)} />
            </Field>
            <Field label="Channels" hint="Comma-separated channel ids.">
              <Input
                value={channels}
                onChange={(e) => setChannels(e.target.value)}
                required
              />
            </Field>
            <Field label="Throttle seconds">
              <Input
                type="number"
                min="0"
                value={throttle}
                onChange={(e) => setThrottle(e.target.value)}
              />
            </Field>
            <Checkbox
              label="Enabled"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            <Row>
              <Button type="submit">Save rule</Button>
              {onCancel ? (
                <Button type="button" onClick={onCancel}>
                  Cancel
                </Button>
              ) : null}
            </Row>
          </Stack>
        </form>
      </Card>
    </Stack>
  );
}

function CatalogCard({
  catalog,
  onUseMatch,
  onInsertPath,
}: {
  catalog: UseSnapshotResult<EventCatalog>;
  onUseMatch: (match: string) => void;
  onInsertPath: (path: string) => void;
}) {
  const [query, setQuery] = useState("");
  const filtering = query.trim().length > 0;

  return (
    <Card title="Event catalog">
      <Stack>
        <Hint>
          Plugins declare the events they publish. Match is what a rule's Match
          field accepts. Insert a payload path into Body — templates only
          interpolate flat keys such as <code>{"{event.payload.body}"}</code>.
        </Hint>
        <Async
          state={catalog}
          loading="Loading events…"
          empty="No events declared."
          isEmpty={(c) => c.events.length === 0}
        >
          {(cat) => {
            const groups = groupedEvents(matching(cat.events, query));
            return (
              <Stack>
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search events…"
                  aria-label="Search events"
                  spellCheck={false}
                />
                <Disclosure
                  summary="Envelope fields"
                  detail={`${cat.envelope.length} on every event`}
                >
                  <Row>
                    {cat.envelope.map((f) => (
                      <Button
                        key={f.path}
                        type="button"
                        size="sm"
                        onClick={() => onInsertPath(f.path)}
                      >
                        {`{${f.path}}`}
                      </Button>
                    ))}
                  </Row>
                </Disclosure>
                {groups.length === 0 ? (
                  <Hint>No event matches “{query}”.</Hint>
                ) : (
                  <div>
                    {groups.map((g) => (
                      <Disclosure
                        // Keyed by the filter too, so toggling a search remounts the group
                        // and its default reapplies: a search opens what it found.
                        key={`${g.source}:${filtering}`}
                        summary={g.name}
                        detail={`${g.events.length} event${g.events.length === 1 ? "" : "s"}`}
                        defaultOpen={filtering}
                      >
                        <div>
                          {g.events.map((ev) => (
                            <CatalogEventRow
                              key={ev.type}
                              event={ev}
                              onUseMatch={onUseMatch}
                              onInsertPath={onInsertPath}
                            />
                          ))}
                        </div>
                      </Disclosure>
                    ))}
                  </div>
                )}
              </Stack>
            );
          }}
        </Async>
      </Stack>
    </Card>
  );
}

/** Free-text search over what an operator would look for: the match, purpose, or a path. */
function matching(events: CatalogEvent[], query: string): CatalogEvent[] {
  const q = query.trim().toLowerCase();
  if (!q) return events;
  return events.filter((ev) => {
    if (
      `${ev.match} ${ev.type} ${ev.name} ${ev.purpose}`
        .toLowerCase()
        .includes(q)
    )
      return true;
    return (ev.fields ?? []).some((f) =>
      `${f.path} ${f.purpose}`.toLowerCase().includes(q),
    );
  });
}

function groupedEvents(events: CatalogEvent[]) {
  const order: string[] = [];
  const by = new Map<string, CatalogEvent[]>();
  for (const ev of events) {
    const list = by.get(ev.source);
    if (list) {
      list.push(ev);
    } else {
      order.push(ev.source);
      by.set(ev.source, [ev]);
    }
  }
  return order.map((source) => ({
    source,
    name: by.get(source)?.[0]?.name ?? source,
    events: by.get(source) ?? [],
  }));
}

function CatalogEventRow({
  event,
  onUseMatch,
  onInsertPath,
}: {
  event: CatalogEvent;
  onUseMatch: (match: string) => void;
  onInsertPath: (path: string) => void;
}) {
  const fields = event.fields ?? [];
  return (
    <Disclosure
      summary={<code>{event.match}</code>}
      detail={
        fields.length === 0
          ? "no fields"
          : `${fields.length} field${fields.length === 1 ? "" : "s"}`
      }
    >
      <Stack>
        <Row>
          <Button
            type="button"
            size="sm"
            onClick={() => onUseMatch(event.match)}
          >
            Use this match
          </Button>
          <Hint>{event.purpose}</Hint>
        </Row>
        {fields.map((f) => (
          <Hint key={f.name}>
            <Button
              type="button"
              size="sm"
              onClick={() => onInsertPath(f.path)}
            >
              {`{${f.path}}`}
            </Button>
            {` · ${f.type} · ${f.purpose}`}
          </Hint>
        ))}
      </Stack>
    </Disclosure>
  );
}

function RuleCard({ rule, onChanged }: { rule: Rule; onChanged: () => void }) {
  const [enabled, setEnabled] = useState(rule.enabled);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  if (editing) {
    return (
      <RuleForm
        initial={rule}
        onChanged={() => {
          setEditing(false);
          onChanged();
        }}
        onCancel={() => setEditing(false)}
      />
    );
  }

  return (
    <Card
      title={rule.id}
      actions={
        <>
          <Badge>{rule.match}</Badge>
          <Badge tone={enabled ? "ok" : "warn"}>{enabled ? "on" : "off"}</Badge>
        </>
      }
    >
      <Stack>
        <Hint>
          {rule.title} → {rule.channels.join(", ")}
          {rule.throttleSeconds > 0
            ? ` · throttle ${rule.throttleSeconds}s`
            : ""}
        </Hint>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Row>
          <Checkbox
            label="Enabled"
            checked={enabled}
            onChange={(e) => {
              const next = e.target.checked;
              setEnabled(next);
              void api
                .put(
                  `/api/admin/notifications/rules/${encodeURIComponent(rule.id)}`,
                  {
                    ...rule,
                    enabled: next,
                  },
                )
                .then(onChanged)
                .catch((err) => {
                  setEnabled(rule.enabled);
                  setError(formatErr(err));
                });
            }}
          />
          <Button size="sm" onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button
            size="sm"
            onClick={() => {
              void api
                .del(
                  `/api/admin/notifications/rules/${encodeURIComponent(rule.id)}`,
                )
                .then(onChanged)
                .catch((err) => setError(formatErr(err)));
            }}
          >
            Delete
          </Button>
        </Row>
      </Stack>
    </Card>
  );
}
