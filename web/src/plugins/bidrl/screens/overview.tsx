import {
  Callout,
  Card,
  Grid,
  Hint,
  Link,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
} from "@cc/ui";
import { useOverview } from "../data";
import { BidrlTabs, StatLink } from "../chrome";
import { Notices } from "../actions";
import { LotBrowser } from "../lots";
import { AutomationStrip } from "../schedule";

/**
 * The front door. It answers "is there anything to look at" without being a fourth
 * listing of the same table: a few counts, the widest gaps, what closes next, and a link
 * into the catalog for each. Everything here is a shortcut to a filtered catalog URL.
 */
export function Overview() {
  const snap = useOverview();
  const disabled = snap.error instanceof PluginDisabledError;
  const stats = snap.status === "ready" ? snap.data.stats : null;
  const deals = snap.status === "ready" ? snap.data.deals : [];
  const closing = snap.status === "ready" ? snap.data.closing : [];

  return (
    <Page>
      <PageHeader
        title="BIDRL"
        lede="Score lots from photos, then review the best opportunities."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <AutomationStrip />
        {snap.status === "loading" ? <Loading label="Loading…" /> : null}
        {snap.status === "error" && !disabled ? (
          <Callout tone="danger">{snap.error.message}</Callout>
        ) : null}
        {stats != null && stats.lots === 0 ? (
          <Card title="Start here">
            <Stack>
              <Hint>
                Nothing is collected yet. Collect an auction, then scan it —
                scanning is what reads the photographs and produces comparables.
              </Hint>
              <div className="bidrl-actions">
                <Link
                  to="/bidrl/auctions"
                  className="cc-button cc-button--primary"
                >
                  Collect an auction
                </Link>
              </div>
            </Stack>
          </Card>
        ) : null}
        {stats != null && stats.lots > 0 ? (
          <>
            <Grid density="metric">
              <StatLink
                to="/bidrl/auctions"
                label="Auctions"
                value={String(stats.auctions)}
              />
              <StatLink
                to="/bidrl/lots"
                label="Lots"
                value={String(stats.lots)}
                hint={`${stats.live} still open`}
              />
              <StatLink
                to="/bidrl/lots?filter=deals"
                label="Priced"
                value={String(stats.priced)}
                hint={
                  stats.unscanned > 0
                    ? `${stats.unscanned} never scanned`
                    : "all scanned"
                }
                tone={stats.priced > 0 ? "ok" : "neutral"}
              />
              <StatLink
                to="/bidrl/lots?ending=soon"
                label="Ending in 24h"
                value={String(stats.ending)}
                tone={stats.ending > 0 ? "warn" : "neutral"}
              />
            </Grid>
            <Card
              title="Widest gaps"
              actions={
                <Link to="/bidrl/lots?filter=deals">See all priced lots</Link>
              }
            >
              <LotBrowser
                lots={deals}
                empty="No lot has a comparable yet. Scan an auction, then reprice a lot whose photos show a model."
                view="grid"
                groupSimilar={false}
              />
            </Card>
            <Card
              title="Closing next"
              actions={
                <Link to="/bidrl/lots?ending=soon">See everything ending</Link>
              }
            >
              <LotBrowser
                lots={closing}
                empty="Nothing collected closes in the next week."
                view="table"
                groupSimilar={false}
              />
            </Card>
          </>
        ) : null}
      </Stack>
    </Page>
  );
}
