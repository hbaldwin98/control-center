/** Daily kWh as stacked peak / off-peak bars. */
import { useState } from "react";
import type { Day } from "./model";
import { kwh, money } from "./model";

export function UsageChart({ days, label }: { days: Day[]; label: string }) {
  const ordered = [...days].reverse();
  const [active, setActive] = useState<number | null>(null);
  const peak = Math.max(1, ...ordered.map((day) => day.kwh));
  const selected = active == null ? null : ordered[active];
  return (
    <div className="tid-chart">
      <div className="tid-chart__header">
        <strong>{label}</strong>
        <span className="tid-chart__readout" aria-live="polite">
          {selected ? `${selected.day}: ${kwh(selected.kwh)} kWh${money(selected.costCents) ? ` · ${money(selected.costCents)}` : ""}` : "Hover or focus a day"}
        </span>
      </div>
      <div className="tid-chart__frame">
        <div className="tid-chart__axis" aria-hidden="true">
          <span>{kwh(peak)}</span>
          <span>{kwh(peak / 2)}</span>
          <span>0</span>
        </div>
        <div className="tid-chart__plot" onPointerLeave={() => setActive(null)}>
          {ordered.map((day, index) => {
            const totalHeight = Math.max(2, (day.kwh / peak) * 100);
            const hasSplit = day.onPeakKwh != null || day.offPeakKwh != null;
            const onShare = hasSplit && day.kwh > 0 ? ((day.onPeakKwh ?? 0) / day.kwh) * 100 : 0;
            return (
              <button
                className="tid-chart__day"
                key={day.day}
                type="button"
                aria-label={`${day.day}, ${kwh(day.kwh)} kilowatt-hours${day.onPeakKwh == null ? "" : `, ${kwh(day.onPeakKwh)} on peak`}${day.offPeakKwh == null ? "" : `, ${kwh(day.offPeakKwh)} off peak`}`}
                onFocus={() => setActive(index)}
                onBlur={() => setActive(null)}
                onPointerEnter={() => setActive(index)}
              >
                <span className="tid-chart__bar" style={{ height: `${totalHeight}%` }}>
                  {hasSplit ? (
                    <>
                      <span className="tid-chart__off-peak" style={{ height: `${100 - onShare}%` }} />
                      <span className="tid-chart__on-peak" style={{ height: `${onShare}%` }} />
                    </>
                  ) : (
                    <span className="tid-chart__total" />
                  )}
                </span>
              </button>
            );
          })}
        </div>
      </div>
      <div className="tid-chart__dates" aria-hidden="true">
        <span>{ordered[0]?.day}</span>
        <span>{ordered.at(-1)?.day}</span>
      </div>
      <div className="tid-chart__legend">
        <span><i className="tid-chart__swatch tid-chart__swatch--on" />On peak</span>
        <span><i className="tid-chart__swatch tid-chart__swatch--off" />Off peak</span>
      </div>
    </div>
  );
}
