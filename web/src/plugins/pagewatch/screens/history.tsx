/** The plugin's one screen: the target, its last check, and the run of them. */
import { useState } from "react";
import {
  Button,
  Callout,
  Card,
  EmptyState,
  Loading,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  Stack,
} from "@cc/ui";
import type { UseSnapshotResult } from "@cc/ui";
import {
  type Check,
  type ChecksPage,
} from "../model";
import { api } from "../api";
import { useChecks } from "../data";
import { AttentionCallout, CheckTable, LatestMetrics, TargetCard } from "../checks";

export function HistoryHeader({ busy, disabled, runCheck }: {
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

export function HistoryMessages({ message, error, disabled }: {
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
          Page Watch is disabled. Set a daily budget and enable it on the <a href="/plugins/pagewatch/settings">plugin screen</a>.
        </Callout>
      ) : null}
      <PluginAIHint pluginId="pagewatch" />
    </>
  );
}

export function SnapshotError({ snap, disabled }: {
  snap: UseSnapshotResult<ChecksPage>;
  disabled: boolean;
}) {
  if (snap.status !== "error" || disabled) return null;
  return <Callout tone="danger">{snap.error.message}</Callout>;
}

export function CheckHistory({ checks }: { checks: Check[] }) {
  if (checks.length === 0) {
    return <EmptyState>No checks yet. Enable the plugin and press Check now, or wait for the six-hour schedule.</EmptyState>;
  }
  return <Card title="Check history"><CheckTable checks={checks} /></Card>;
}

export function ReadyHistory({ page }: { page: ChecksPage }) {
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

export function HistoryBody({ snap }: { snap: UseSnapshotResult<ChecksPage> }) {
  if (snap.status === "loading") return <Loading label="Loading page checks…" />;
  if (snap.status !== "ready") return null;
  return <ReadyHistory page={snap.data} />;
}

export function History() {
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
