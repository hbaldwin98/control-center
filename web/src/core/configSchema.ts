/**
 * Turns the host's JSON Schema subset into a flat list of form fields.
 *
 * The host validates nested objects and arrays, so the form renders them: an object
 * becomes a titled group of dotted-path fields, and an array of strings becomes one
 * comma-separated input. A plugin whose only switch lives inside an object — bidrl's
 * `automation.enabled` — is otherwise unreachable from the UI, which is worse than
 * ugly: it is a setting nobody can change.
 *
 * Anything still outside the subset the form can draw is skipped, and a saved document
 * is built over the stored one so a skipped key is left alone rather than erased.
 */

/** How deep a nested object is still worth drawing as a group of fields. */
const maxDepth = 3;

export type ConfigField = {
  /** Dotted path, and the key this field's draft value is held under. */
  name: string;
  /** The same path, split, for reading and writing the config document. */
  path: string[];
  type: "string" | "number" | "integer" | "boolean" | "strings";
  title: string;
  /** Title of the object this field lives in, for a heading. Absent at the top level. */
  group?: string;
  description?: string;
  enumValues?: string[];
  required: boolean;
  maxLength?: number;
  minLength?: number;
  minimum?: number;
  maximum?: number;
  /** For a string array: what each item has to look like, and how many are allowed. */
  itemPattern?: string;
  maxItems?: number;
};

export function fieldsFromSchema(schema: unknown): ConfigField[] {
  return collect(schema, [], undefined, 1);
}

function collect(schema: unknown, prefix: string[], group: string | undefined, depth: number): ConfigField[] {
  if (!isRecord(schema) || schema.type !== "object" || !isRecord(schema.properties)) {
    return [];
  }
  const required = new Set(
    Array.isArray(schema.required) ? schema.required.filter((x): x is string => typeof x === "string") : [],
  );
  const fields: ConfigField[] = [];
  for (const [key, raw] of Object.entries(schema.properties)) {
    if (!isRecord(raw)) continue;
    const path = [...prefix, key];
    const title = typeof raw.title === "string" && raw.title ? raw.title : key;

    if (raw.type === "object") {
      // A nested object is a group, not a value: its own title labels the fields
      // inside it. Its description is left to the fields, which each carry their own.
      if (depth < maxDepth) fields.push(...collect(raw, path, title, depth + 1));
      continue;
    }

    const field = scalarField(raw, path, group, title, required.has(key));
    if (field) fields.push(field);
  }
  return fields;
}

/** One drawable value, or nothing if the schema node is outside what the form draws. */
function scalarField(
  raw: Record<string, unknown>,
  path: string[],
  group: string | undefined,
  title: string,
  required: boolean,
): ConfigField | null {
  const name = path.join(".");
  if (raw.type === "array") {
    // Only a list of strings. A list of anything else needs a real editor, and
    // guessing at one would put values in front of the host it will reject.
    if (!isRecord(raw.items) || raw.items.type !== "string") return null;
    const field: ConfigField = { name, path, type: "strings", title, required };
    if (group !== undefined) field.group = group;
    if (typeof raw.description === "string") field.description = raw.description;
    if (typeof raw.items.pattern === "string") field.itemPattern = raw.items.pattern;
    if (typeof raw.maxItems === "number") field.maxItems = raw.maxItems;
    return field;
  }
  const type = raw.type;
  if (type !== "string" && type !== "number" && type !== "integer" && type !== "boolean") {
    return null;
  }
  const enumValues = Array.isArray(raw.enum)
    ? raw.enum.filter((x): x is string => typeof x === "string")
    : undefined;
  const field: ConfigField = { name, path, type, title, required };
  if (group !== undefined) field.group = group;
  if (typeof raw.description === "string") field.description = raw.description;
  if (enumValues && enumValues.length > 0) field.enumValues = enumValues;
  if (typeof raw.maxLength === "number") field.maxLength = raw.maxLength;
  if (typeof raw.minLength === "number") field.minLength = raw.minLength;
  if (typeof raw.minimum === "number") field.minimum = raw.minimum;
  if (typeof raw.maximum === "number") field.maximum = raw.maximum;
  return field;
}

export function draftFromConfig(
  fields: ConfigField[],
  value: Record<string, unknown> | null | undefined,
): Record<string, string | boolean> {
  const draft: Record<string, string | boolean> = {};
  for (const f of fields) {
    const v = readPath(value, f.path);
    if (f.type === "boolean") {
      draft[f.name] = v === true;
    } else if (f.type === "strings") {
      draft[f.name] = Array.isArray(v) ? v.filter((x) => typeof x === "string").join(", ") : "";
    } else if (v === undefined || v === null) {
      draft[f.name] = "";
    } else {
      draft[f.name] = String(v);
    }
  }
  return draft;
}

/**
 * Builds the JSON document the admin API stores. Empty numeric fields are omitted.
 *
 * `base` is the stored document, and the draft is written over a copy of it: the form
 * does not draw every key a schema may hold, and a config editor that silently drops
 * what it could not draw would lose settings on an unrelated save.
 */
export function documentFromDraft(
  fields: ConfigField[],
  draft: Record<string, string | boolean>,
  base?: Record<string, unknown> | null | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = base ? (structuredClone(base) as Record<string, unknown>) : {};
  for (const f of fields) {
    const v = draft[f.name];
    if (f.type === "boolean") {
      writePath(out, f.path, v === true);
      continue;
    }
    const s = typeof v === "string" ? v : String(v ?? "");
    if (f.type === "strings") {
      // Commas, spaces, and newlines all separate: an operator pasting a list should
      // not have to care which the form wanted.
      const items = s.split(/[\s,]+/).filter((x) => x !== "");
      writePath(out, f.path, items);
      continue;
    }
    if (f.type === "string") {
      if (s !== "" || f.required) writePath(out, f.path, s);
      else deletePath(out, f.path);
      continue;
    }
    if (s.trim() === "") {
      if (f.required) writePath(out, f.path, 0);
      else deletePath(out, f.path);
      continue;
    }
    const n = Number(s);
    if (!Number.isFinite(n)) {
      throw new Error(`${f.title} must be a number`);
    }
    writePath(out, f.path, f.type === "integer" ? Math.trunc(n) : n);
  }
  return out;
}

function readPath(value: unknown, path: string[]): unknown {
  let at: unknown = value;
  for (const key of path) {
    if (!isRecord(at)) return undefined;
    at = at[key];
  }
  return at;
}

function writePath(out: Record<string, unknown>, path: string[], value: unknown): void {
  let at = out;
  for (const key of path.slice(0, -1)) {
    const next = at[key];
    if (!isRecord(next)) at[key] = {};
    at = at[key] as Record<string, unknown>;
  }
  at[path[path.length - 1] as string] = value;
}

/** Removes an omitted optional value, leaving the object it lived in in place. */
function deletePath(out: Record<string, unknown>, path: string[]): void {
  let at: Record<string, unknown> = out;
  for (const key of path.slice(0, -1)) {
    const next = at[key];
    if (!isRecord(next)) return;
    at = next;
  }
  delete at[path[path.length - 1] as string];
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
