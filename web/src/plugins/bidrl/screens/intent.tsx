/** Intent search. */
import {
  useState,
} from "react";
import {
  Button,
  Callout,
  Card,
  EmptyState,
  Field,
  Hint,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
  Textarea,
} from "@cc/ui";
import {
  INTENT_EXAMPLES,
} from "../model";
import { api } from "../api";
import { useIntent, useLotView } from "../data";
import { ViewToggle, BidrlTabs } from "../chrome";
import { Notices } from "../actions";
import { LotBrowser } from "../lots";

export function IntentSearch() {
  const [intentDraft, setIntentDraft] = useState("");
  const [intentBusy, setIntentBusy] = useState(false);
  const [intentError, setIntentError] = useState<string | null>(null);
  const [view, setView] = useLotView();
  const intent = useIntent();
  const disabled = intent.error instanceof PluginDisabledError;
  const search = intent.status === "ready" ? intent.data.search : null;
  const intentLots = intent.status === "ready" ? intent.data.lots : [];
  const intentRunning = search?.status === "queued" || search?.status === "running" || intentBusy;

  const ask = async () => {
    const query = intentDraft.trim();
    if (!query || intentRunning) return;
    setIntentBusy(true);
    setIntentError(null);
    try {
      await api.post("/intent", { query });
      intent.reload();
    } catch (err) {
      setIntentError(err instanceof Error ? err.message : String(err));
    } finally {
      setIntentBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL / Actions"
        title="Intent"
        lede="Ask for what you actually want — camping gear, not the word camp."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={intentError} disabled={disabled} />
        <Card title="Intent">
          <Stack>
            <Hint>
              One chat call turns your intent into related gear (headlamp, lantern, tent —
              not only the word you typed). Those words are embedded and ranked against
              collected titles and descriptions. Photo identifications count too when a
              lot has already been scanned.
            </Hint>
            <Field label="What are you looking to do?">
              <Textarea
                value={intentDraft}
                onChange={(e) => setIntentDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void ask();
                  }
                }}
                placeholder="Things that would help me camp"
                disabled={disabled || intentRunning}
                rows={2}
                aria-label="Intent search"
              />
            </Field>
            <div className="bidrl-actions">
              <Button variant="primary" disabled={disabled || intentRunning || !intentDraft.trim()} onClick={() => void ask()}>
                {intentRunning ? "Matching…" : "Ask"}
              </Button>
              <Hint>Enter to ask, Shift+Enter for a new line.</Hint>
            </div>
            <div className="bidrl-examples">
              <span className="bidrl-examples__label">Try</span>
              {INTENT_EXAMPLES.map((example) => (
                <button
                  key={example}
                  type="button"
                  className="bidrl-chip"
                  disabled={disabled || intentRunning}
                  onClick={() => setIntentDraft(example)}
                >
                  {example}
                </button>
              ))}
            </div>
            {search?.status === "failed" && search.lastError ? (
              <Callout tone="danger">{search.lastError}</Callout>
            ) : null}
            {search && search.status !== "failed" ? (
              <Hint>
                {search.status === "ready"
                  ? `${search.hitCount} match${search.hitCount === 1 ? "" : "es"} of ${search.scanned} lots`
                  : `Looking through ${search.scanned || "collected"} lots…`}
                {search.skipped > 0 ? ` · ${search.skipped} from title or description` : ""}
                {search.query ? ` · “${search.query}”` : ""}
              </Hint>
            ) : null}
          </Stack>
        </Card>
        {search && (search.status === "ready" || intentRunning) ? (
          <Card title="Matches" actions={<ViewToggle value={view} onChange={setView} />}>
            {intent.status === "loading" || intentRunning ? <Loading label="Matching lots to your intent…" /> : null}
            {intent.status === "error" && !disabled ? <Callout tone="danger">{intent.error.message}</Callout> : null}
            {intent.status === "ready" && search.status === "ready" ? (
              <LotBrowser
                lots={intentLots}
                empty="Nothing in the collected lots serves that intent."
                view={view}
                groupSimilar={false}
              />
            ) : null}
          </Card>
        ) : (
          <Card title="Matches">
            <EmptyState>
              Describe what you want to do and BIDRL ranks every collected lot against it. Nothing is
              fetched from BidRL and no photograph is sent — this reads titles and descriptions you
              have already collected.
            </EmptyState>
          </Card>
        )}
      </Stack>
    </Page>
  );
}
