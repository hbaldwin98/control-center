/** Queueing a job, and reporting what came of it. */
import {
  useState,
  type ReactNode,
} from "react";
import {
  Callout,
  Link,
  PluginAIHint,
} from "@cc/ui";


/**
 * Every button here queues a job rather than doing the work, so every button owes the
 * same answer: what was queued, under which job number, and where to watch it. Screens
 * used to post and say nothing, which read as a dead button.
 */
export function useAction() {
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<ReactNode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = async (key: string, label: string, call: () => Promise<{ jobId?: number } | void>) => {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      const result = (await call()) ?? {};
      setNotice(
        result.jobId != null ? (
          <>
            {label} queued as <Link to="/jobs">job {result.jobId}</Link>. This page updates as it lands.
          </>
        ) : (
          `${label} done.`
        ),
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  return { busy, notice, error, run, setNotice, setError };
}

export function Notices({
  message,
  error,
  disabled,
}: {
  message: ReactNode | null;
  error: string | null;
  disabled: boolean;
}) {
  return (
    <>
      {message ? <Callout tone="ok">{message}</Callout> : null}
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {disabled ? (
        <Callout>
          BIDRL is disabled. Enable it on the <Link to="/plugins/bidrl/settings">plugin screen</Link>.
        </Callout>
      ) : null}
      <PluginAIHint pluginId="bidrl" />
    </>
  );
}
