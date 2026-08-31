import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ApiError,
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Page,
  PageHeader,
  Row,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";

type Job = {
  id: number;
  pluginId: string;
  name: string;
  args: string;
  state: string;
  attempt: number;
  maxAttempts: number;
  progress: number;
  progressMessage: string;
  lastError: string;
  lastErrorClass: string;
  cancelReason: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  logs?: { id: number; attempt: number; at: string; line: string }[];
};

const STATES = ["", "pending", "running", "retry_wait", "cancel_requested", "succeeded", "failed", "dead", "cancelled"];

/** The queue and its history, with per-job progress, logs, and cancellation. */
export function Jobs() {
  const [state, setState] = useState("");
  const [params, setParams] = useSearchParams();
  // The plugin filter lives in the URL so a plugin's detail screen can link straight to
  // its own queue, and so that link stays shareable.
  const plugin = params.get("plugin") ?? "";
  const load = useCallback(
    (signal: AbortSignal) => {
      const q = new URLSearchParams();
      if (state) q.set("state", state);
      if (plugin) q.set("plugin", plugin);
      const query = q.toString();
      return api.snapshot<Job[]>(`/api/jobs${query ? `?${query}` : ""}`, { signal });
    },
    [state, plugin],
  );
  const list = useSnapshot<Job[]>(load, { events: "core.job.**" });
  const openId = Number(params.get("id") || "") || null;
  const setOpenId = (id: number | null) => {
    const next = new URLSearchParams(params);
    if (id == null) next.delete("id");
    else next.set("id", String(id));
    setParams(next, { replace: true });
  };
  const clearPlugin = () => {
    const next = new URLSearchParams(params);
    next.delete("plugin");
    setParams(next, { replace: true });
  };

  return (
    <Page>
      <PageHeader
        title="Jobs"
        lede="The queue and its history. REST loads the snapshot; the shared stream refetches after every job event."
      />
      <Stack>
        <Row>
          <label className="cc-field" style={{ minWidth: 180 }}>
            <span className="cc-field__label">State</span>
            <select
              className="cc-input"
              value={state}
              onChange={(e) => setState(e.target.value)}
              aria-label="Filter by job state"
            >
              <option value="">All</option>
              {STATES.filter(Boolean).map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
          {plugin ? (
            <Button type="button" onClick={clearPlugin}>
              Plugin: {plugin} ✕
            </Button>
          ) : null}
        </Row>

        {list.status === "error" ? <Callout tone="danger">{list.error.message}</Callout> : null}
        {list.status === "loading" ? (
          <EmptyState>Loading…</EmptyState>
        ) : list.status === "ready" && list.data.length === 0 ? (
          <EmptyState>{plugin ? `No jobs for ${plugin}.` : "No jobs yet."}</EmptyState>
        ) : list.status === "ready" ? (
          <Card>
            <div style={{ overflowX: "auto" }}>
              <table className="cc-table">
                <thead>
                  <tr>
                    <th>ID</th>
                    <th>Plugin</th>
                    <th>Name</th>
                    <th>State</th>
                    <th>Attempt</th>
                    <th>Progress</th>
                    <th>Created</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {list.data.map((j) => (
                    <JobRow
                      key={j.id}
                      job={j}
                      open={openId === j.id}
                      onToggle={() => setOpenId(openId === j.id ? null : j.id)}
                      onChanged={list.reload}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}

function JobRow({
  job,
  open,
  onToggle,
  onChanged,
}: {
  job: Job;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const cancellable = job.state === "pending" || job.state === "running" || job.state === "retry_wait";
  return (
    <>
      <tr>
        <td className="cc-num">{job.id}</td>
        <td>
          <code>{job.pluginId}</code>
        </td>
        <td>
          <code>{job.name}</code>
        </td>
        <td>
          <StateBadge state={job.state} />
          {job.lastError ? <div className="cc-field__hint">{job.lastError}</div> : null}
          {job.cancelReason ? <div className="cc-field__hint">{job.cancelReason}</div> : null}
        </td>
        <td className="cc-num">
          {job.attempt}/{job.maxAttempts}
        </td>
        <td>
          {Math.round(job.progress * 100)}%{job.progressMessage ? ` · ${job.progressMessage}` : ""}
        </td>
        <td title={job.createdAt}>{formatWhen(job.createdAt)}</td>
        <td>
          <Row>
            <Button type="button" onClick={onToggle}>
              {open ? "Hide" : "Logs"}
            </Button>
            {cancellable ? <CancelButton id={job.id} onChanged={onChanged} /> : null}
          </Row>
        </td>
      </tr>
      {open ? (
        <tr>
          <td colSpan={8}>
            <JobDetail id={job.id} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

function CancelButton({ id, onChanged }: { id: number; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <>
      <Button
        type="button"
        variant="danger"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          setError(null);
          void api
            .post(`/api/jobs/${id}/cancel`)
            .then(onChanged)
            .catch((err) =>
              setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err)),
            )
            .finally(() => setBusy(false));
        }}
      >
        Cancel
      </Button>
      {error ? <span className="cc-field__hint">{error}</span> : null}
    </>
  );
}

function JobDetail({ id }: { id: number }) {
  const load = useCallback(
    (signal: AbortSignal) => api.snapshot<Job>(`/api/jobs/${id}`, { signal }),
    [id],
  );
  const detail = useSnapshot<Job>(load, { events: "core.job.**" });
  if (detail.status === "loading") return <div className="cc-field__hint">Loading logs…</div>;
  if (detail.status === "error") return <Callout tone="danger">{detail.error.message}</Callout>;
  const logs = detail.data.logs ?? [];
  return (
    <Stack>
      <div className="cc-field__hint">
        {detail.data.startedAt ? `started ${formatWhen(detail.data.startedAt)}` : "not started"}
        {detail.data.finishedAt ? ` · finished ${formatWhen(detail.data.finishedAt)}` : ""}
      </div>
      {logs.length === 0 ? (
        <div className="cc-field__hint">No log lines.</div>
      ) : (
        <pre style={{ margin: 0, whiteSpace: "pre-wrap", fontFamily: "var(--mono)", fontSize: 12.5 }}>
          {logs.map((l) => `${formatWhen(l.at)}  ${l.line}`).join("\n")}
        </pre>
      )}
    </Stack>
  );
}

function StateBadge({ state }: { state: string }) {
  const tone = useMemo(() => {
    switch (state) {
      case "succeeded":
        return "ok" as const;
      case "failed":
      case "dead":
        return "danger" as const;
      case "running":
      case "cancel_requested":
        return "warn" as const;
      default:
        return "neutral" as const;
    }
  }, [state]);
  return <Badge tone={tone}>{state}</Badge>;
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
