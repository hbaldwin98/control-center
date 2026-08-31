import { useCallback, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ActionsHeader,
  ApiError,
  Async,
  Badge,
  Button,
  Card,
  Field,
  Hint,
  LogBlock,
  Page,
  PageHeader,
  Row,
  Select,
  Stack,
  Table,
  Time,
  Toolbar,
  api,
  formatProgress,
  formatTime,
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

const STATES = [
  "pending",
  "running",
  "retry_wait",
  "cancel_requested",
  "succeeded",
  "failed",
  "dead",
  "cancelled",
];

/** The queue and its history, with per-job progress, logs, and cancellation. */
export function Jobs() {
  const [state, setState] = useState("");
  const load = useCallback(
    (signal: AbortSignal) => {
      const q = state ? `?state=${encodeURIComponent(state)}` : "";
      return api.snapshot<Job[]>(`/api/jobs${q}`, { signal });
    },
    [state],
  );
  const list = useSnapshot<Job[]>(load, { events: "core.job.**" });
  const [params, setParams] = useSearchParams();
  const openId = Number(params.get("id") || "") || null;
  const setOpenId = (id: number | null) => {
    const next = new URLSearchParams(params);
    if (id == null) next.delete("id");
    else next.set("id", String(id));
    setParams(next, { replace: true });
  };

  return (
    <Page>
      <PageHeader
        title="Jobs"
        lede="The queue and its history. REST loads the snapshot; the shared stream refetches after every job event."
      />
      <Stack>
        <Toolbar>
          <Field label="State">
            <Select
              value={state}
              onChange={(e) => setState(e.target.value)}
              aria-label="Filter by job state"
            >
              <option value="">All states</option>
              {STATES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          </Field>
        </Toolbar>

        <Async state={list} loading="Loading jobs…" empty={state ? `No ${state} jobs.` : "No jobs yet."}>
          {(jobs) => (
            <Card>
              <Table
                head={
                  <>
                    <th className="cc-num">ID</th>
                    <th>Plugin</th>
                    <th>Name</th>
                    <th>State</th>
                    <th className="cc-num">Attempt</th>
                    <th>Progress</th>
                    <th>Created</th>
                    <ActionsHeader />
                  </>
                }
              >
                {jobs.map((j) => (
                  <JobRow
                    key={j.id}
                    job={j}
                    open={openId === j.id}
                    onToggle={() => setOpenId(openId === j.id ? null : j.id)}
                    onChanged={list.reload}
                  />
                ))}
              </Table>
            </Card>
          )}
        </Async>
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
          {job.lastError ? <Hint>{job.lastError}</Hint> : null}
          {job.cancelReason ? <Hint>{job.cancelReason}</Hint> : null}
        </td>
        <td className="cc-num">
          {job.attempt}/{job.maxAttempts}
        </td>
        <td className="cc-nowrap">{formatProgress(job.progress, job.progressMessage)}</td>
        <td>
          <Time iso={job.createdAt} />
        </td>
        <td className="cc-table__actions">
          <Row>
            <Button type="button" size="sm" pressed={open} onClick={onToggle}>
              {open ? "Hide" : "Logs"}
            </Button>
            {cancellable ? <CancelButton id={job.id} onChanged={onChanged} /> : null}
          </Row>
        </td>
      </tr>
      {open ? (
        <tr className="cc-table__detail">
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
        size="sm"
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
  const load = useCallback((signal: AbortSignal) => api.snapshot<Job>(`/api/jobs/${id}`, { signal }), [id]);
  const detail = useSnapshot<Job>(load, { events: "core.job.**" });
  return (
    <Async state={detail} loading="Loading logs…">
      {(job) => {
        const logs = job.logs ?? [];
        return (
          <Stack>
            <Hint>
              {job.startedAt ? `Started ${formatTime(job.startedAt)}` : "Not started"}
              {job.finishedAt ? ` · finished ${formatTime(job.finishedAt)}` : ""}
            </Hint>
            {logs.length === 0 ? (
              <Hint>No log lines.</Hint>
            ) : (
              <LogBlock>{logs.map((l) => `${formatTime(l.at)}  ${l.line}`).join("\n")}</LogBlock>
            )}
          </Stack>
        );
      }}
    </Async>
  );
}

function StateBadge({ state }: { state: string }) {
  switch (state) {
    case "succeeded":
      return <Badge tone="ok">{state}</Badge>;
    case "failed":
    case "dead":
      return <Badge tone="danger">{state}</Badge>;
    case "running":
    case "cancel_requested":
      return <Badge tone="warn">{state}</Badge>;
    default:
      return <Badge>{state}</Badge>;
  }
}
