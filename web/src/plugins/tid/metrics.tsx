/** Month-to-date use, against last month or the last billing period. */
import { useState } from "react";
import {
  Button,
  Grid,
  Metric,
  Stack,
} from "@cc/ui";
import type { Summary } from "./model";
import { kwh, money, delta } from "./model";

export function Metrics({ data }: { data: Summary }) {
  const [comparison, setComparison] = useState<"month" | "billing">("billing");
  const billingAvailable = data.lastBillingPeriodFrom !== "";
  const prior = comparison === "billing" && billingAvailable
    ? data.lastBillingPeriodKwh
    : data.lastMonthKwh;
  const priorLabel = comparison === "billing" && billingAvailable
    ? "Last billing period"
    : "Last month";
  return (
    <Stack>
      <div className="tid-comparison" aria-label="Usage comparison">
        <span className="cc-hint">Compare with</span>
        <Button pressed={comparison === "month"} onClick={() => setComparison("month")}>Last month</Button>
        <Button
          pressed={comparison === "billing"}
          disabled={!billingAvailable}
          onClick={() => setComparison("billing")}
        >
          Last billing period
        </Button>
      </div>
      <Grid density="metric">
        <Metric
          label="This month"
          value={`${kwh(data.monthKwh)} kWh`}
          hint={delta(data.monthKwh, prior, priorLabel)}
        />
        <Metric
          label={priorLabel}
          value={`${kwh(prior)} kWh`}
          hint={comparison === "billing" && billingAvailable
            ? `${data.lastBillingPeriodFrom} to ${data.lastBillingPeriodTo}`
            : undefined}
        />
        <Metric label="Same month last year" value={`${kwh(data.lastYearKwh)} kWh`} />
        <Metric
          label="Peak day, last 30 days"
          value={`${kwh(data.peakKwh)} kWh`}
          hint={data.peakDay || undefined}
        />
        {money(data.estCostCents) ? (
          <Metric label="Month cost" value={money(data.estCostCents) ?? "—"} />
        ) : null}
      </Grid>
    </Stack>
  );
}
