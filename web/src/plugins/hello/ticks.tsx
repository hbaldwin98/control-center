/** The tick history as a table. Shared by the screen and the detail panel. */
import { Dash, Table, Time } from "@cc/ui";
import type { Tick } from "./model";

export function TickTable({ ticks, limit }: { ticks: Tick[]; limit?: number }) {
  return (
    <Table
      head={
        <>
          <th>When</th>
          <th>Note</th>
          <th>AI</th>
        </>
      }
    >
      {(limit ? ticks.slice(0, limit) : ticks).map((t) => (
        <tr key={t.id}>
          <td>
            <Time iso={t.at} />
          </td>
          <td>{t.note}</td>
          <td>{t.aiText || <Dash />}</td>
        </tr>
      ))}
    </Table>
  );
}
