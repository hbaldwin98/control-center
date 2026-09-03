import { useCallback, useState, type FormEvent } from "react";
import {
  ActionsHeader,
  ApiError,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Field,
  Hint,
  Input,
  LogBlock,
  Page,
  PageHeader,
  Row,
  Select,
  Stack,
  Table,
  Textarea,
  Time,
  Toolbar,
  api,
  useSnapshot,
} from "@cc/ui";

type Profile = {
  id: string;
  name: string;
  workspaceRoot: string;
  acceptsInstruction: boolean;
};

type Session = {
  id: number;
  profileId: string;
  profileName: string;
  title: string;
  workspace: string;
  state: string;
  exitCode: number | null;
  error: string;
  stopReason: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  output?: { id: number; stream: string; text: string; createdAt: string }[];
  outputTruncated?: boolean;
};

type HarnessSnapshot = { profiles: Profile[]; sessions: Session[] };

export function Sessions() {
  const load = useCallback(
    (signal: AbortSignal) => api.snapshot<HarnessSnapshot>("/api/harness", { signal }),
    [],
  );
  const snapshot = useSnapshot(load, { events: "core.harness.**" });

  return (
    <Page>
      <PageHeader
        title="Sessions"
        lede="Run configured coding-agent commands, inspect retained output, and stop active processes."
      />
      <Async state={snapshot} loading="Loading harness sessions…">
        {(data) => (
          <Stack>
            <CreateSession profiles={data.profiles} onCreated={snapshot.reload} />
            <SessionTable sessions={data.sessions} onChanged={snapshot.reload} />
          </Stack>
        )}
      </Async>
    </Page>
  );
}

function CreateSession({ profiles, onCreated }: { profiles: Profile[]; onCreated: () => void }) {
  const [profileId, setProfileId] = useState("");
  const [workspace, setWorkspace] = useState("");
  const [title, setTitle] = useState("");
  const [instruction, setInstruction] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const selectedId = profileId || profiles[0]?.id || "";
  const selected = profiles.find((profile) => profile.id === selectedId);

  if (profiles.length === 0) {
    return <Callout tone="warn">No harness profiles are configured. Add one under <code>harness.profiles</code>.</Callout>;
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    void api
      .post<Session>("/api/harness", { profileId: selectedId, workspace, title, instruction })
      .then(() => {
        setTitle("");
        setInstruction("");
        onCreated();
      })
      .catch((err) => setError(errorMessage(err)))
      .finally(() => setBusy(false));
  };

  return (
    <Card title="Start session">
      <form onSubmit={submit}>
        <Stack>
          <Toolbar>
            <Field label="Profile" hint={selected ? `Root: ${selected.workspaceRoot}` : undefined}>
              <Select value={selectedId} onChange={(event) => setProfileId(event.currentTarget.value)}>
                {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
              </Select>
            </Field>
            <Field label="Workspace" hint="Relative to the profile root">
              <Input value={workspace} onChange={(event) => setWorkspace(event.currentTarget.value)} placeholder="." />
            </Field>
            <Field label="Title">
              <Input value={title} onChange={(event) => setTitle(event.currentTarget.value)} placeholder={selected?.name} />
            </Field>
          </Toolbar>
          {selected?.acceptsInstruction ? (
            <Field label="Initial instruction">
              <Textarea
                value={instruction}
                onChange={(event) => setInstruction(event.currentTarget.value)}
                rows={3}
                maxLength={32 << 10}
              />
            </Field>
          ) : null}
          <Row>
            <Button type="submit" variant="primary" disabled={busy}>{busy ? "Starting…" : "Start session"}</Button>
          </Row>
          {error ? <Callout tone="danger">{error}</Callout> : null}
        </Stack>
      </form>
    </Card>
  );
}

function SessionTable({ sessions, onChanged }: { sessions: Session[]; onChanged: () => void }) {
  const [openId, setOpenId] = useState<number | null>(null);
  if (sessions.length === 0) return <Hint>No harness sessions yet.</Hint>;
  return (
    <Card>
      <Table head={<><th className="cc-num">ID</th><th>Session</th><th>Profile</th><th>State</th><th>Created</th><ActionsHeader /></>}>
        {sessions.map((session) => (
          <SessionRow
            key={session.id}
            session={session}
            open={openId === session.id}
            onToggle={() => setOpenId(openId === session.id ? null : session.id)}
            onChanged={onChanged}
          />
        ))}
      </Table>
    </Card>
  );
}

function SessionRow({ session, open, onToggle, onChanged }: { session: Session; open: boolean; onToggle: () => void; onChanged: () => void }) {
  const active = session.state === "starting" || session.state === "running" || session.state === "stopping";
  return (
    <>
      <tr>
        <td className="cc-num">{session.id}</td>
        <td><strong>{session.title}</strong><Hint><code>{session.workspace}</code></Hint></td>
        <td>{session.profileName}</td>
        <td><StateBadge state={session.state} />{session.error ? <Hint>{session.error}</Hint> : null}</td>
        <td><Time iso={session.createdAt} /></td>
        <td className="cc-table__actions">
          <Row>
            <Button type="button" size="sm" pressed={open} onClick={onToggle}>{open ? "Hide" : "Output"}</Button>
            {active && session.state !== "stopping" ? <StopButton id={session.id} onChanged={onChanged} /> : null}
          </Row>
        </td>
      </tr>
      {open ? <tr className="cc-table__detail"><td colSpan={6}><SessionDetail id={session.id} /></td></tr> : null}
    </>
  );
}

function StopButton({ id, onChanged }: { id: number; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return <>
    <Button type="button" size="sm" variant="danger" disabled={busy} onClick={() => {
      setBusy(true);
      setError(null);
      void api.post(`/api/harness/${id}/stop`).then(onChanged).catch((err) => setError(errorMessage(err))).finally(() => setBusy(false));
    }}>{busy ? "Stopping…" : "Stop"}</Button>
    {error ? <span className="cc-field__hint">{error}</span> : null}
  </>;
}

function SessionDetail({ id }: { id: number }) {
  const load = useCallback((signal: AbortSignal) => api.snapshot<Session>(`/api/harness/${id}`, { signal }), [id]);
  const detail = useSnapshot(load, { events: "core.harness.**" });
  return <Async state={detail} loading="Loading output…">{(session) => {
    const output = session.output ?? [];
    return <Stack>
      <Hint>
        {session.startedAt ? <>Started <Time iso={session.startedAt} /></> : "Not started"}
        {session.finishedAt ? <> · Finished <Time iso={session.finishedAt} /></> : null}
        {session.exitCode !== null ? ` · Exit ${session.exitCode}` : ""}
      </Hint>
      {session.outputTruncated ? <Callout tone="warn">Older output was removed by the retention limit.</Callout> : null}
      {output.length === 0 ? <Hint>No output captured.</Hint> : <LogBlock>{output.map((chunk) => chunk.stream === "stderr" ? `[stderr] ${chunk.text}` : chunk.text).join("")}</LogBlock>}
    </Stack>;
  }}</Async>;
}

function StateBadge({ state }: { state: string }) {
  if (state === "exited") return <Badge tone="ok">{state}</Badge>;
  if (state === "failed" || state === "interrupted") return <Badge tone="danger">{state}</Badge>;
  if (state === "running" || state === "starting" || state === "stopping") return <Badge tone="warn">{state}</Badge>;
  return <Badge>{state}</Badge>;
}

function errorMessage(err: unknown): string {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err);
}
