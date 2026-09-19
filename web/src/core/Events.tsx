import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import {
  ActionsHeader,
  Badge,
  Button,
  Callout,
  Dash,
  EmptyState,
  Field,
  Hint,
  Input,
  Loading,
  LogBlock,
  Page,
  PageHeader,
  Panel,
  Row,
  Stack,
  Table,
  Time,
  api,
  isValidPattern,
  matchesPattern,
  useEvents,
  useQueryState,
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

/** The named patterns an operator reaches for most, offered as one-click filters. */
const PRESETS: readonly (readonly [string, string])[] = [
  ["**", "All"],
  ["core.plugin.**", "Plugins"],
  ["core.job.**", "Jobs"],
  ["core.ai.**", "AI"],
  ["core.browser.**", "Browser"],
  ["**.alert", "Alerts"],
  ["core.notification.**", "Notifications"],
];

/** The live event log, filterable by dot-segment pattern. */
export function Events() {
  const [patternParam, setPatternParam] = useQueryState("pattern", "**");
  const pattern = isValidPattern(patternParam) ? patternParam : "**";
  const [draft, setDraft] = useState(patternParam);
  const draftValid = isValidPattern(draft);

  useEffect(() => setDraft(patternParam), [patternParam]);

  const loadPage = useCallback(
    (signal: AbortSignal) =>
      api.snapshot<EventsPage>(
        `/api/events?pattern=${encodeURIComponent(pattern)}&limit=200`,
        {
          signal,
        },
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

  const [expanded, setExpanded] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  const apply = (next: string) => {
    setDraft(next);
    setPatternParam(next);
  };

  return (
    <Page>
      <PageHeader
        eyebrow="System / Events"
        title="Event stream"
        lede="Recent signals across plugins, collections, and actions."
      />

      <Stack>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (draftValid) setPatternParam(draft);
          }}
        >
          <Field
            label="Pattern"
            hint="* matches one segment, ** matches zero or more."
          >
            <Row>
              <Input
                mono
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                aria-label="Event pattern"
                aria-invalid={!draftValid}
                spellCheck={false}
                className="cc-input--pattern"
              />
              <Button
                type="submit"
                variant="primary"
                disabled={!draftValid || draft === pattern}
              >
                Filter
              </Button>
            </Row>
          </Field>
        </form>
        <Row>
          {PRESETS.map(([p, label]) => (
            <Button
              key={p}
              type="button"
              size="sm"
              pressed={pattern === p}
              onClick={() => apply(p)}
            >
              {label}
            </Button>
          ))}
        </Row>

        {!draftValid ? (
          <Callout tone="danger">
            <code>{draft}</code> is not a valid pattern. Use dot-separated
            segments, <code>*</code>, or <code>**</code>.
          </Callout>
        ) : null}

        <Subscribers />

        {history.status === "error" ? (
          <Callout tone="danger">{history.error.message}</Callout>
        ) : history.status === "loading" ? (
          <Loading label="Loading events…" />
        ) : rows.length === 0 ? (
          <EmptyState>
            No events match <code>{pattern}</code> yet.
          </EmptyState>
        ) : (
          <Panel
            title="Latest events"
            subhead={`${rows.length} event${rows.length === 1 ? "" : "s"} match ${pattern}`}
          >
            <Table
              head={
                <>
                  <th className="cc-num">ID</th>
                  <th>Type</th>
                  <th>Source</th>
                  <th>Subject</th>
                  <th>At</th>
                  <th>Payload</th>
                </>
              }
            >
              {rows.map((e) => {
                const json = JSON.stringify(e.payload);
                const open = expanded.has(e.id);
                return (
                  <Fragment key={e.id}>
                    <tr>
                      <td className="cc-num">{e.id}</td>
                      <td>
                        <code>{e.type}</code>
                      </td>
                      <td>{e.source || <Dash />}</td>
                      <td>{e.subject || <Dash />}</td>
                      <td>
                        <Time iso={e.createdAt} timeOnly />
                      </td>
                      <td>
                        {json === undefined || json === "null" ? (
                          <Dash />
                        ) : (
                          <button
                            type="button"
                            className="cc-disclose"
                            aria-expanded={open}
                            onClick={() => toggle(e.id)}
                          >
                            <code className="cc-truncate">{json}</code>
                          </button>
                        )}
                      </td>
                    </tr>
                    {open ? (
                      <tr className="cc-table__detail">
                        <td colSpan={6}>
                          <LogBlock>
                            {JSON.stringify(e.payload, null, 2)}
                          </LogBlock>
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </Table>
          </Panel>
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
    (signal: AbortSignal) =>
      api.snapshot<SubscriberStatus[]>("/api/events/subscribers", { signal }),
    [],
  );
  const subs = useSnapshot<SubscriberStatus[]>(load, {
    events: "core.event.subscription_paused",
  });
  const [busy, setBusy] = useState<string | null>(null);

  const act = async (name: string, action: "retry" | "skip") => {
    setBusy(name);
    try {
      await api.post(
        `/api/events/subscribers/${encodeURIComponent(name)}/${action}`,
      );
      subs.reload();
    } finally {
      setBusy(null);
    }
  };

  if (subs.status !== "ready" || subs.data.length === 0) return null;

  const unhealthy = subs.data.filter((s) => !s.healthy).length;

  return (
    <Panel
      title="Durable subscribers"
      actions={
        unhealthy > 0 ? (
          <Badge tone="danger">{unhealthy} paused</Badge>
        ) : (
          <Badge tone="ok">all active</Badge>
        )
      }
    >
      <Table
        head={
          <>
            <th>Name</th>
            <th>Pattern</th>
            <th className="cc-num">Position</th>
            <th>State</th>
            <ActionsHeader />
          </>
        }
      >
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
                <Badge tone="danger">paused on {s.failedEventId}</Badge>
              )}
              {s.lastError ? <Hint>{s.lastError}</Hint> : null}
            </td>
            <td className="cc-table__actions">
              {s.healthy ? null : (
                <Row>
                  <Button
                    size="sm"
                    disabled={busy === s.name}
                    onClick={() => void act(s.name, "retry")}
                  >
                    Retry
                  </Button>
                  <Button
                    size="sm"
                    disabled={busy === s.name}
                    onClick={() => void act(s.name, "skip")}
                  >
                    Skip
                  </Button>
                </Row>
              )}
            </td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}
