/**
 * Schema-backed plugin config. The host validates the same subset on PUT.
 */
import { useState } from "react";
import { ApiError, Button, Field, Input, Row, api } from "@cc/ui";
import { documentFromDraft, draftFromConfig, fieldsFromSchema } from "./configSchema";

export function ConfigForm({
  pluginId,
  schema,
  value,
  disabled,
  onSaved,
  onError,
}: {
  pluginId: string;
  schema: unknown;
  value: Record<string, unknown> | null | undefined;
  disabled: boolean;
  onSaved: () => void;
  onError: (message: string | null) => void;
}) {
  const fields = fieldsFromSchema(schema);
  const [draft, setDraft] = useState(() => draftFromConfig(fields, value));
  const [busy, setBusy] = useState(false);

  if (fields.length === 0) return null;

  const set = (name: string, next: string | boolean) => {
    setDraft((prev) => ({ ...prev, [name]: next }));
  };

  const save = async () => {
    setBusy(true);
    onError(null);
    try {
      const body = documentFromDraft(fields, draft);
      await api.put(`/api/admin/plugins/${encodeURIComponent(pluginId)}/config`, body);
      onSaved();
    } catch (err) {
      onError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="cc-stack"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      {fields.map((f) => {
        const hint = [f.description, f.maxLength ? `max ${f.maxLength}` : ""].filter(Boolean).join(" · ");
        const raw = draft[f.name];
        const text = typeof raw === "string" ? raw : "";
        if (f.type === "boolean") {
          return (
            <label key={f.name} className="cc-check">
              <input
                type="checkbox"
                checked={draft[f.name] === true}
                onChange={(e) => set(f.name, e.target.checked)}
                disabled={disabled || busy}
              />
              <span>
                {f.title}
                {hint ? <span className="cc-field__hint"> {hint}</span> : null}
              </span>
            </label>
          );
        }
        if (f.enumValues) {
          return (
            <Field key={f.name} label={f.title} hint={hint || ""}>
              <select
                className="cc-input"
                value={text}
                onChange={(e) => set(f.name, e.target.value)}
                disabled={disabled || busy}
                aria-label={f.title}
              >
                {!f.required ? <option value="">—</option> : null}
                {f.enumValues.map((opt) => (
                  <option key={opt} value={opt}>
                    {opt}
                  </option>
                ))}
              </select>
            </Field>
          );
        }
        return (
          <Field key={f.name} label={f.title} hint={hint || ""}>
            <Input
              value={text}
              onChange={(e) => set(f.name, e.target.value)}
              maxLength={f.maxLength}
              inputMode={f.type === "string" ? undefined : "decimal"}
              disabled={disabled || busy}
              aria-label={f.title}
            />
          </Field>
        );
      })}
      <Row>
        <Button type="submit" disabled={disabled || busy}>
          Save config
        </Button>
      </Row>
    </form>
  );
}
