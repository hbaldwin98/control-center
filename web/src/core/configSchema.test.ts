import { describe, expect, it } from "vitest";
import { documentFromDraft, draftFromConfig, fieldsFromSchema } from "./configSchema";

const helloSchema = {
  type: "object",
  additionalProperties: false,
  properties: {
    note: {
      type: "string",
      description: "A label recorded with every tick.",
      maxLength: 80,
    },
  },
};

describe("fieldsFromSchema", () => {
  it("renders the hello note field", () => {
    const fields = fieldsFromSchema(helloSchema);
    expect(fields).toEqual([
      {
        name: "note",
        type: "string",
        title: "note",
        description: "A label recorded with every tick.",
        required: false,
        maxLength: 80,
      },
    ]);
  });

  it("skips arrays, nested objects, and unknown types", () => {
    const fields = fieldsFromSchema({
      type: "object",
      required: ["ok"],
      properties: {
        ok: { type: "boolean", title: "Ready" },
        nested: { type: "object", properties: { x: { type: "string" } } },
        tags: { type: "array", items: { type: "string" } },
        kind: { type: "string", enum: ["a", "b"] },
      },
    });
    expect(fields.map((f) => f.name)).toEqual(["ok", "kind"]);
    expect(fields[0]?.required).toBe(true);
    expect(fields[1]?.enumValues).toEqual(["a", "b"]);
  });

  it("returns nothing for a non-object schema", () => {
    expect(fieldsFromSchema({ type: "string" })).toEqual([]);
    expect(fieldsFromSchema(null)).toEqual([]);
  });
});

describe("draft round-trip", () => {
  it("keeps hello's note and omits an empty optional string", () => {
    const fields = fieldsFromSchema(helloSchema);
    const draft = draftFromConfig(fields, { note: "hello" });
    expect(draft).toEqual({ note: "hello" });
    expect(documentFromDraft(fields, { note: "" })).toEqual({});
    expect(documentFromDraft(fields, { note: "hi" })).toEqual({ note: "hi" });
  });
});
