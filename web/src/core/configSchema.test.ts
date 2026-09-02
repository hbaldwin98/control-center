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
        path: ["note"],
        type: "string",
        title: "note",
        description: "A label recorded with every tick.",
        required: false,
        maxLength: 80,
      },
    ]);
  });

  it("draws nested objects and string lists, and skips what it cannot draw", () => {
    const fields = fieldsFromSchema({
      type: "object",
      required: ["ok"],
      properties: {
        ok: { type: "boolean", title: "Ready" },
        nested: {
          type: "object",
          title: "Automation",
          properties: { x: { type: "string" }, on: { type: "boolean" } },
        },
        tags: { type: "array", items: { type: "string", pattern: "^[0-9]+$" }, maxItems: 20 },
        // A list of objects needs a real editor; guessing at one would send the host
        // values it rejects.
        rules: { type: "array", items: { type: "object" } },
        kind: { type: "string", enum: ["a", "b"] },
      },
    });
    expect(fields.map((f) => f.name)).toEqual(["ok", "nested.x", "nested.on", "tags", "kind"]);
    expect(fields[0]?.required).toBe(true);
    // A nested field carries the object's title, so the form can head its group.
    expect(fields[1]?.group).toBe("Automation");
    expect(fields[0]?.group).toBeUndefined();
    expect(fields[3]?.type).toBe("strings");
    expect(fields[3]?.itemPattern).toBe("^[0-9]+$");
    expect(fields[3]?.maxItems).toBe(20);
    expect(fields[4]?.enumValues).toEqual(["a", "b"]);
  });

  // The bug this pins: bidrl's only switch is automation.enabled, and a form that
  // skipped nested objects left it with no control anywhere in the UI.
  it("reaches a switch that only exists inside an object", () => {
    const fields = fieldsFromSchema({
      type: "object",
      properties: {
        automation: {
          type: "object",
          title: "Automation",
          properties: {
            enabled: { type: "boolean", title: "Run on a schedule" },
            affiliateIds: { type: "array", title: "Locations", items: { type: "string" } },
            maxAuctionsPerSweep: { type: "integer", title: "Auctions per tick" },
          },
        },
      },
    });
    expect(fields.map((f) => [f.name, f.type])).toEqual([
      ["automation.enabled", "boolean"],
      ["automation.affiliateIds", "strings"],
      ["automation.maxAuctionsPerSweep", "integer"],
    ]);
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

  const nestedSchema = {
    type: "object",
    properties: {
      searchScope: { type: "string", enum: ["prefer", "all"] },
      automation: {
        type: "object",
        title: "Automation",
        properties: {
          enabled: { type: "boolean" },
          affiliateIds: { type: "array", items: { type: "string" } },
          maxAuctionsPerSweep: { type: "integer" },
        },
      },
    },
  };

  it("reads and writes a value inside an object", () => {
    const fields = fieldsFromSchema(nestedSchema);
    const stored = {
      searchScope: "prefer",
      automation: { enabled: false, affiliateIds: ["19", "7"], maxAuctionsPerSweep: 5 },
    };
    const draft = draftFromConfig(fields, stored);
    expect(draft).toEqual({
      searchScope: "prefer",
      "automation.enabled": false,
      "automation.affiliateIds": "19, 7",
      "automation.maxAuctionsPerSweep": "5",
    });

    const saved = documentFromDraft(
      fields,
      { ...draft, "automation.enabled": true, "automation.affiliateIds": "19 7, 22" },
      stored,
    );
    expect(saved).toEqual({
      searchScope: "prefer",
      automation: { enabled: true, affiliateIds: ["19", "7", "22"], maxAuctionsPerSweep: 5 },
    });
  });

  // Saving one switch must not erase a key the form never drew: with
  // additionalProperties:false the document is written whole, so a dropped key is a
  // lost setting rather than a cosmetic omission.
  it("leaves a key it cannot draw exactly as it found it", () => {
    const fields = fieldsFromSchema(nestedSchema);
    const stored = {
      searchScope: "all",
      rules: [{ kind: "keep" }],
      automation: { enabled: false, affiliateIds: [], maxAuctionsPerSweep: 5, note: "kept" },
    };
    const saved = documentFromDraft(
      fields,
      { ...draftFromConfig(fields, stored), "automation.enabled": true },
      stored,
    );
    expect(saved.rules).toEqual([{ kind: "keep" }]);
    expect((saved.automation as Record<string, unknown>).note).toBe("kept");
    expect((saved.automation as Record<string, unknown>).enabled).toBe(true);
  });

  it("creates the object a nested value lives in when the stored document has none", () => {
    const fields = fieldsFromSchema(nestedSchema);
    const saved = documentFromDraft(fields, { "automation.enabled": true }, {});
    expect(saved).toEqual({ automation: { enabled: true, affiliateIds: [] } });
  });
});
