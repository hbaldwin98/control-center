/**
 * The events this plugin declared it publishes. Lives on the plugin settings tab
 * next to AI, so the operator can see match strings and payload paths without
 * opening Settings or guessing JSON keys.
 *
 * This is reference material, not something read top to bottom: the card names
 * each event on one line and keeps its payload fields folded until asked for.
 */
import { Badge, Card, Disclosure, Hint, Stack } from "@cc/ui";
import type { EventSpec, PluginState } from "./types";

export function PluginEvents({ state }: { state: PluginState }) {
  const events = state.events ?? [];
  if (events.length === 0) return null;

  return (
    <Card title="Events" actions={<Badge>{events.length}</Badge>}>
      <Stack>
        <Hint>
          Notification rules match these types. Title and body interpolate{" "}
          <code>{"{event.subject}"}</code> and <code>{"{event.payload.<field>}"}</code>.
        </Hint>
        <div>
          {events.map((ev) => (
            <EventBlock key={ev.type} event={ev} />
          ))}
        </div>
      </Stack>
    </Card>
  );
}

function EventBlock({ event }: { event: EventSpec }) {
  const fields = event.fields ?? [];
  return (
    <Disclosure
      summary={<code>{event.match}</code>}
      detail={fields.length === 0 ? "no fields" : `${fields.length} field${fields.length === 1 ? "" : "s"}`}
    >
      <Stack>
        <Hint>{event.purpose}</Hint>
        {fields.length === 0 ? (
          <Hint>No payload fields. Use {"{event.subject}"} or {"{event.type}"}.</Hint>
        ) : (
          fields.map((f) => (
            <Hint key={f.name}>
              <code>{`{${f.path}}`}</code>
              {` · ${f.type} · ${f.purpose}`}
            </Hint>
          ))
        )}
      </Stack>
    </Disclosure>
  );
}
