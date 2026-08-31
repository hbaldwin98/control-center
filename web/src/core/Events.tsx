import { useCallback, useMemo, useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Input,
  Page,
  PageHeader,
  Stack,
  api,
  isValidPattern,
  matchesPattern,
  useEvents,
  useSnapshot,
  type Event,
} from "@cc/ui";

type EventsPage = {
  events: Event[];
  oldestRetainedId: string;
};

type SubscriberStatus = {
  name: string;
  pattern: string;
  lastEventId: number;
  state: "active" | "paused";
  failedEventId: number;
  attempts: number;
  retryAt: string | null;
  lastError: string;
  healthy: boolean;
};

/** The live event log, filterable by dot-segment pattern. */
export function Events() {
  const [draft, setDraft] = useState("**");
  const [pattern, setPattern] = useState("**");
  const draftValid = isValidPattern(draft);

  const loadPage = useCallback(
    (signal: AbortSignal) =>
      api.snapshot<EventsPage>(
        `/api/events?pattern=${encodeURIComponent(pattern)}&limit=200`,
        { signal },
      ),
    [pattern],
  );

  // The history snapshot; `useEvents` supplies everything committed after it.
  const history = useSnapshot<EventsPage>(loadPage);
  const live = useEvents(pattern);

  const rows = useMemo(() => {
    const seen = new Set<string>();
    const all: Event[] = [];
    for (const e of history.data?.events ?? []) {
      if (!seen.has(e.id)) {
        seen.add(e.id);
        all.push(e);
      }
    }
    for (const e of live) {
      if (!seen.has(e.id) && matchesPattern(pattern, e.type)) {
        seen.add(e.id);
        all.push(e);
      }
    }
    // Newest first: an operator reads the top of this page.
    return all.reverse();
  }, [history.data, live, pattern]);

  return (
    <Page>
      <PageHeader
        title="Events"
        lede="The persisted log. REST loads the history; the shared stream applies everything after it."
      />

      <Stack>
        <form
          className="cc-row"
          onSubmit={(e) => {
            e.preventDefault();
            if (draftValid) setPattern(draft);
          }}
        >
          <Input
            mono
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            aria-label="Event pattern"
            spellCheck={false}
            style={{ maxWidth: 360 }}
          />
          <Button type="submit" variant="primary" disabled={!draftValid}>
            Filter
          </Button>
          <span className="cc-field__hint">
            <code>*</code> matches one segment, <code>**</code> matches zero or more.
          </span>
        </form>

        {!draftValid ? <Callout tone="danger">Not a valid pattern.</Callout> : null}

        <div className="cc-row">
          {(
            [
              ["**", "all"],
              ["core.plugin.**", "plugins"],
              ["core.job.**", "jobs"],
              ["core.ai.**", "ai"],
              ["core.browser.**", "browser"],
              ["**.alert", "alerts"],
            ] as const
          ).map(([p, label]) => (
            <Button
              key={p}
              type="button"
              onClick={() => {
                setDraft(p);
                setPattern(p);
              }}
            >
              {label}
            </Button>
          ))}
        </div>
        {history.status === "error" ? (
          <Callout tone="danger">{history.error.message}</Callout>
        ) : null}

        <Subscribers />

        {history.status === "loading" ? (
          <EmptyState>Loading…</EmptyState>
        ) : rows.length === 0 ? (
          <EmptyState>No events match this pattern yet.</EmptyState>
        ) : (
          <Card title={`${rows.length} event${rows.length === 1 ? "" : "s"}`}>
            <div style={{ overflowX: "auto" }}>
              <table className="cc-table">
                <thead>
                  <tr>
                    <th>ID</th>
                    <th>Type</th>
                    <th>Source</th>
                    <th>Subject</th>
                    <th>At</th>
                    <th>Payload</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((e) => (
                    <tr key={e.id}>
                      <td className="cc-num">{e.id}</td>
                      <td>
                        <code>{e.type}</code>
                      </td>
                      <td>{e.source}</td>
                      <td>{e.subject || "—"}</td>
                      <td title={e.createdAt}>{formatTime(e.createdAt)}</td>
                      <td>
                        <code className="cc-truncate">{JSON.stringify(e.payload)}</code>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}
      </Stack>
    </Page>
  );
}

/**
 * Durable subscriber health. A paused subscriber stopped on a poison event and later
 * events are not passing it, so it is shown here rather than buried in a log.
 */
function Subscribers() {
  const load = useCallback(
    (signal: AbortSignal) => api.snapshot<SubscriberStatus[]>("/api/events/subscribers", { signal }),
    [],
  );
  const subs = useSnapshot<SubscriberStatus[]>(load, {
    events: "core.event.subscription_paused",
  });
  const [busy, setBusy] = useState<string | null>(null);

  const act = async (name: string, action: "retry" | "skip") => {
    setBusy(name);
    try {
      await api.post(`/api/events/subscribers/${encodeURIComponent(name)}/${action}`);
      subs.reload();
    } finally {
      setBusy(null);
    }
  };

  if (subs.status !== "ready" || subs.data.length === 0) return null;

  return (
    <Card title="Durable subscribers">
      <div style={{ overflowX: "auto" }}>
        <table className="cc-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Pattern</th>
              <th>Position</th>
              <th>State</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {subs.data.map((s) => (
              <tr key={s.name}>
                <td>
                  <code>{s.name}</code>
                </td>
                <td>
                  <code>{s.pattern}</code>
                </td>
                <td className="cc-num">{s.lastEventId}</td>
                <td>
                  {s.healthy ? (
                    <Badge tone="ok">active</Badge>
                  ) : (
                    <Badge tone="danger">
                      paused on {s.failedEventId}
                    </Badge>
                  )}
                  {s.lastError ? (
                    <div className="cc-field__hint">{s.lastError}</div>
                  ) : null}
                </td>
                <td>
                  {s.healthy ? null : (
                    <div className="cc-row">
                      <Button disabled={busy === s.name} onClick={() => void act(s.name, "retry")}>
                        Retry
                      </Button>
                      <Button disabled={busy === s.name} onClick={() => void act(s.name, "skip")}>
                        Skip
                      </Button>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}
