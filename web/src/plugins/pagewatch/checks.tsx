/** One check, and a run of them, rendered. */
import {
  Badge,
  Callout,
  Card,
  Dash,
  Grid,
  Hint,
  Metric,
  Money,
  RelativeTime,
  Row,
  Stack,
  Table,
  Time,
} from "@cc/ui";
import {
  statusLabel,
  statusTone,
  type Check,
  type ChecksPage,
} from "./model";

export function CheckTable({ checks, limit }: { checks: Check[]; limit?: number }) {
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

export function AttentionCallout({ latest }: { latest: Check | undefined }) {
  if (latest?.status !== "attention") return null;
  return (
    <Callout tone="danger">
      Expected text {JSON.stringify(latest.expectedText)} was not found during the latest check.
    </Callout>
  );
}

export function LatestMetrics({ latest, schedule }: { latest: Check | undefined; schedule: string }) {
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

export function TargetCard({ page, latest }: { page: ChecksPage; latest: Check | undefined }) {
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
