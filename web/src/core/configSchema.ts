/**
 * Turns the host's JSON Schema subset into a flat list of form fields.
 *
 * Nested objects, arrays, and unsupported keywords are skipped so the Plugins screen
 * only renders controls the host will accept.
 */

export type ConfigField = {
  name: string;
  type: "string" | "number" | "integer" | "boolean";
  title: string;
  description?: string;
  enumValues?: string[];
  required: boolean;
  maxLength?: number;
  minLength?: number;
  minimum?: number;
  maximum?: number;
};

export function fieldsFromSchema(schema: unknown): ConfigField[] {
  if (!isRecord(schema) || schema.type !== "object" || !isRecord(schema.properties)) {
    return [];
  }
  const required = new Set(
    Array.isArray(schema.required) ? schema.required.filter((x): x is string => typeof x === "string") : [],
  );
  const fields: ConfigField[] = [];
  for (const [name, raw] of Object.entries(schema.properties)) {
    if (!isRecord(raw)) continue;
    const type = raw.type;
    if (type !== "string" && type !== "number" && type !== "integer" && type !== "boolean") {
      continue;
    }
    const enumValues = Array.isArray(raw.enum)
      ? raw.enum.filter((x): x is string => typeof x === "string")
      : undefined;
    const field: ConfigField = {
      name,
      type,
      title: typeof raw.title === "string" && raw.title ? raw.title : name,
      required: required.has(name),
    };
    if (typeof raw.description === "string") field.description = raw.description;
    if (enumValues && enumValues.length > 0) field.enumValues = enumValues;
    if (typeof raw.maxLength === "number") field.maxLength = raw.maxLength;
    if (typeof raw.minLength === "number") field.minLength = raw.minLength;
    if (typeof raw.minimum === "number") field.minimum = raw.minimum;
    if (typeof raw.maximum === "number") field.maximum = raw.maximum;
    fields.push(field);
  }
  return fields;
}

export function draftFromConfig(
  fields: ConfigField[],
  value: Record<string, unknown> | null | undefined,
): Record<string, string | boolean> {
  const draft: Record<string, string | boolean> = {};
  for (const f of fields) {
    const v = value?.[f.name];
    if (f.type === "boolean") {
      draft[f.name] = v === true;
    } else if (v === undefined || v === null) {
      draft[f.name] = "";
    } else {
      draft[f.name] = String(v);
    }
  }
  return draft;
}

/** Builds the JSON document the admin API stores. Empty numeric fields are omitted. */
export function documentFromDraft(
  fields: ConfigField[],
  draft: Record<string, string | boolean>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    const v = draft[f.name];
    if (f.type === "boolean") {
      out[f.name] = v === true;
      continue;
    }
    const s = typeof v === "string" ? v : String(v ?? "");
    if (f.type === "string") {
      if (s !== "" || f.required) out[f.name] = s;
      continue;
    }
    if (s.trim() === "") {
      if (f.required) out[f.name] = f.type === "integer" ? 0 : 0;
      continue;
    }
    const n = Number(s);
    if (!Number.isFinite(n)) {
      throw new Error(`${f.title} must be a number`);
    }
    out[f.name] = f.type === "integer" ? Math.trunc(n) : n;
  }
  return out;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
