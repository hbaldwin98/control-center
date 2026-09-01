/** Page Watch: a low-cost browser, AI, storage, event, and job canary. */
import { useCallback, useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  Loading,
  Metric,
  Money,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  RelativeTime,
  Row,
  Stack,
  Table,
  Time,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";
import {
  applyCheckEvent,
  eventBoundary,
  statusLabel,
  statusTone,
  type Check,
  type ChecksPage,
} from "./model";

const api = pluginApi("pagewatch");

function useChecks(): UseSnapshotResult<ChecksPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<ChecksPage>("/checks", signal);
    return { data, asOfEventId: eventBoundary(data.checks) };
  }, []);

  return useSnapshot(load, {
    events: "pagewatch.check.completed",
    apply: applyCheckEvent,
  });
}

function CheckTable({ checks, limit }: { checks: Check[]; limit?: number }) {
  const rows = limit ? checks.slice(0, limit) : checks;
  return (
    <Table
      head={
        <>
          <th>When</th>
          <th>Status</th>
          <th>Summary</th>
          <th className="cc-num">Browser</th>
          <th className="cc-num">AI</th>
          <th className="cc-num">Cost</th>
        </>
      }
    >
      {rows.map((check) => (
        <tr key={String(check.eventId)}>
          <td><Time iso={check.checkedAt} /></td>
          <td><Badge tone={statusTone(check.status)}>{statusLabel(check.status)}</Badge></td>
          <td>{check.summary || <Dash />}</td>
          <td className="cc-num">{check.browserMs} ms</td>
          <td className="cc-num">
            {check.aiRan ? `${check.inputTokens + check.outputTokens} tokens / ${check.aiMs} ms` : <Dash />}
          </td>
          <td className="cc-num"><Money microUsd={check.costMicroUsd} /></td>
        </tr>
      ))}
    </Table>
  );
}

function HistoryHeader({ busy, disabled, runCheck }: {
  busy: boolean;
  disabled: boolean;
  runCheck: () => Promise<void>;
}) {
  return (
    <PageHeader
      title="Page Watch"
      lede="Checks one public page through the host browser, verifies expected text, detects content drift, and periodically asks the cheap model for a short assessment."
      actions={
        <Button variant="primary" disabled={busy || disabled} onClick={() => void runCheck()}>
          {busy ? "Queueing…" : "Check now"}
        </Button>
      }
    />
  );
}

function HistoryMessages({ message, error, disabled }: {
  message: string | null;
  error: string | null;
  disabled: boolean;
}) {
  return (
    <>
      {message ? <Callout tone="ok">{message}</Callout> : null}
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {disabled ? (
        <Callout>
          Page Watch is disabled. Set a daily budget and enable it on the <a href="/plugins/pagewatch">plugin screen</a>.
        </Callout>
      ) : null}
      <PluginAIHint pluginId="pagewatch" />
    </>
  );
}

function SnapshotError({ snap, disabled }: {
  snap: UseSnapshotResult<ChecksPage>;
  disabled: boolean;
}) {
  if (snap.status !== "error" || disabled) return null;
  return <Callout tone="danger">{snap.error.message}</Callout>;
}

function AttentionCallout({ latest }: { latest: Check | undefined }) {
  if (latest?.status !== "attention") return null;
  return (
    <Callout tone="danger">
      Expected text {JSON.stringify(latest.expectedText)} was not found during the latest check.
    </Callout>
  );
}

function LatestMetrics({ latest, schedule }: { latest: Check | undefined; schedule: string }) {
  if (!latest) {
    return (
      <Grid density="metric">
        <Metric label="Latest result" value="No checks" tone="neutral" />
        <Metric label="Last checked" value={<Dash />} hint={schedule} />
        <Metric label="Browser time" value={<Dash />} />
        <Metric label="Latest AI cost" value={<Dash />} hint="AI skipped" />
      </Grid>
    );
  }
  return (
    <Grid density="metric">
      <Metric label="Latest result" value={statusLabel(latest.status)} tone={statusTone(latest.status)} />
      <Metric label="Last checked" value={<RelativeTime at={latest.checkedAt} />} hint={schedule} />
      <Metric label="Browser time" value={`${latest.browserMs} ms`} />
      <Metric
        label="Latest AI cost"
        value={<Money microUsd={latest.costMicroUsd} compact />}
        hint={latest.aiRan ? `${latest.inputTokens + latest.outputTokens} tokens` : "AI skipped"}
      />
    </Grid>
  );
}

function TargetCard({ page, latest }: { page: ChecksPage; latest: Check | undefined }) {
  return (
    <Card title="Target">
      <Stack>
        <a href={page.targetUrl} target="_blank" rel="noreferrer">{page.targetUrl}</a>
        <Hint>Expected visible text: {page.expectedText || "None configured"}</Hint>
        {latest ? (
          <Row>
            <a href={page.snapshotUrl} target="_blank" rel="noreferrer">Latest normalized text</a>
            <span className="cc-hint">SHA-256 {latest.contentHash.slice(0, 12)}…</span>
          </Row>
        ) : null}
      </Stack>
    </Card>
  );
}

function CheckHistory({ checks }: { checks: Check[] }) {
  if (checks.length === 0) {
    return <EmptyState>No checks yet. Enable the plugin and press Check now, or wait for the six-hour schedule.</EmptyState>;
  }
  return <Card title="Check history"><CheckTable checks={checks} /></Card>;
}

function ReadyHistory({ page }: { page: ChecksPage }) {
  const latest = page.checks[0];
  return (
    <>
      <AttentionCallout latest={latest} />
      <LatestMetrics latest={latest} schedule={page.schedule} />
      <TargetCard page={page} latest={latest} />
      <CheckHistory checks={page.checks} />
    </>
  );
}

function HistoryBody({ snap }: { snap: UseSnapshotResult<ChecksPage> }) {
  if (snap.status === "loading") return <Loading label="Loading page checks…" />;
  if (snap.status !== "ready") return null;
  return <ReadyHistory page={snap.data} />;
}

function History() {
  const snap = useChecks();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const runCheck = async () => {
    setBusy(true);
    setMessage(null);
    setError(null);
    try {
      const result = await api.post<{ jobId: number }>("/checks");
      setMessage(`Check queued as job ${result.jobId}. This page will update when it completes.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = snap.error instanceof PluginDisabledError;
  return (
    <Page>
      <HistoryHeader busy={busy} disabled={disabled} runCheck={runCheck} />
      <Stack>
        <HistoryMessages message={message} error={error} disabled={disabled} />
        <SnapshotError snap={snap} disabled={disabled} />
        <HistoryBody snap={snap} />
      </Stack>
    </Page>
  );
}

function tileErrorMessage(error: Error): string {
  return error instanceof PluginDisabledError ? "Disabled." : error.message;
}

function emptyTileMessage(enabled: boolean): string {
  return enabled ? "No checks yet." : "Disabled, and no checks recorded.";
}

function ReadyTile({ latest, enabled }: { latest: Check; enabled: boolean }) {
  return (
    <Stack>
      <Row>
        <Badge tone={enabled ? statusTone(latest.status) : "neutral"}>{statusLabel(latest.status)}</Badge>
        <span className="cc-hint"><RelativeTime at={latest.checkedAt} prefix="checked" /></span>
      </Row>
      <Hint>{latest.summary}</Hint>
    </Stack>
  );
}

function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useChecks();
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") return <Hint>{tileErrorMessage(snap.error)}</Hint>;
  const latest = snap.data.checks[0];
  if (!latest) return <Hint>{emptyTileMessage(enabled)}</Hint>;
  return <ReadyTile latest={latest} enabled={enabled} />;
}

function Detail() {
  const snap = useChecks();
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.checks.length === 0) return <Hint>No checks yet.</Hint>;
  return <CheckTable checks={snap.data.checks} limit={8} />;
}

const pagewatch: PluginModule = {
  id: "pagewatch",
  nav: [{ path: "/pagewatch", label: "Page Watch" }],
  routes: [{ path: "/pagewatch", element: <History /> }],
  dashboard: {
    summary: "Checks one public page for expected text and content drift every six hours.",
    live: ["pagewatch.check.completed"],
    tile: Tile,
    detail: Detail,
  },
};

export default pagewatch;
