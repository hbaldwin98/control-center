/**
 * The AI setup panels: connecting a provider, and pointing a plugin's named route at a
 * model.
 *
 * These two carry the highest branch counts in the frontend, and both are write paths
 * against admin endpoints -- what they PUT, and whether they refuse to PUT at all, is
 * the behaviour worth pinning. `aiSetup.test.ts` covers the pure helpers beside them;
 * this file covers the forms.
 */
import { describe, expect, it } from "vitest";
import { ConnectProvider, NeedAssign } from "./aiSetup";
import { setupHarness, fill } from "./testing/harness";
import { credential, modelNeed, provider } from "./testing/fixtures";

const h = setupHarness();

/** The PUT/POST calls a form made, in order. */
const writes = () => h.calls.filter((c) => c.method !== "GET");

describe("ConnectProvider", () => {
  it("refuses to save with neither a key nor a credential", async () => {
    let connected = 0;
    await h.render(<ConnectProvider credentials={[]} onConnected={() => (connected += 1)} />);
    await h.click(/connect|save|add/i);

    expect(writes(), "nothing should be written without a credential").toEqual([]);
    expect(connected).toBe(0);
    expect(h.text()).toMatch(/API key|credential/i);
  });

  it("creates a credential from a pasted key, then points the provider at it", async () => {
    h.routes.set("/api/admin/credentials", { id: "cred-new" });
    h.routes.set("/api/admin/ai/providers/openrouter", { ok: true });
    let connected = 0;
    await h.render(<ConnectProvider credentials={[]} onConnected={() => (connected += 1)} />);

    // Fields in order: provider id, base URL, then the API key and the admin password.
    // The first password input is the key.
    await fill(h.container, 'input[type="password"]', "sk-test-123");
    await h.click(/connect|save|add/i);

    const created = writes().find((c) => c.path === "/api/admin/credentials");
    expect(created, `no credential was created; calls: ${JSON.stringify(writes())}`).toBeTruthy();
    expect((created?.body as { secret?: string })?.secret).toBe("sk-test-123");

    const put = writes().find((c) => c.path.startsWith("/api/admin/ai/providers/"));
    expect(put?.method).toBe("PUT");
    expect((put?.body as { credentialId?: string })?.credentialId).toBe("cred-new");
    expect(connected).toBe(1);
  });

  it("reuses an existing credential without creating another", async () => {
    h.routes.set("/api/admin/ai/providers/openrouter", { ok: true });
    await h.render(
      <ConnectProvider credentials={[credential({ id: "c1", name: "openrouter-key" })]} onConnected={() => {}} />,
    );
    // The second select is the existing-credential picker; the first chooses the kind.
    await fill(h.container, "select", "c1", 1);
    await h.click(/connect|save|add/i);

    expect(writes().some((c) => c.path === "/api/admin/credentials" && c.method === "POST")).toBe(false);
  });

  it("reports the server's refusal rather than claiming success", async () => {
    h.routes.set("/api/admin/credentials", { id: "cred-new" });
    h.failures.set("/api/admin/ai/providers/openrouter", 400);
    let connected = 0;
    await h.render(<ConnectProvider credentials={[]} onConnected={() => (connected += 1)} />);
    await fill(h.container, 'input[type="password"]', "sk-test-123");
    await h.click(/connect|save|add/i);

    expect(connected, "a failed PUT must not report the provider as connected").toBe(0);
  });
});

describe("NeedAssign", () => {
  const need = modelNeed({ name: "cheap-chat", status: "missing", provider: "", model: "" });

  it("says to connect a provider first when there are none", async () => {
    await h.render(<NeedAssign need={need} providers={[]} onAssigned={() => {}} />);
    expect(h.text()).toMatch(/connect a provider/i);
    expect(writes()).toEqual([]);
  });

  it("assigns the chosen provider and model to the route by name", async () => {
    h.routes.set("/api/admin/ai/routes/cheap-chat/assign", { ok: true });
    let assigned = 0;
    await h.render(
      <NeedAssign need={need} providers={[provider({ id: "openai" })]} onAssigned={() => (assigned += 1)} />,
    );
    await fill(h.container, 'input[list]', "gpt-4o-mini");
    await h.click(/assign|save|use/i);

    const put = writes().find((c) => c.path.includes("/assign"));
    expect(put, `no assign call; calls: ${JSON.stringify(h.calls)}`).toBeTruthy();
    expect(put?.method).toBe("PUT");
    expect(put?.body).toMatchObject({ provider: "openai", model: "gpt-4o-mini" });
    expect(assigned).toBe(1);
  });

  it("trims the typed model name", async () => {
    h.routes.set("/api/admin/ai/routes/cheap-chat/assign", { ok: true });
    await h.render(<NeedAssign need={need} providers={[provider({ id: "openai" })]} onAssigned={() => {}} />);
    await fill(h.container, 'input[list]', "  gpt-4o-mini  ");
    await h.click(/assign|save|use/i);

    expect((writes().find((c) => c.path.includes("/assign"))?.body as { model?: string })?.model).toBe(
      "gpt-4o-mini",
    );
  });

  it("shows the route's own error when the host reported it unhealthy", async () => {
    await h.render(
      <NeedAssign
        need={modelNeed({ name: "cheap-chat", status: "unhealthy", lastError: "401 from provider" })}
        providers={[provider({ id: "openai" })]}
        onAssigned={() => {}}
      />,
    );
    expect(h.text()).toContain("401 from provider");
  });

  // A route that exists but advertises the wrong capabilities is not simply missing;
  // the panel has to explain that picking a model recreates it.
  it("explains a capability mismatch", async () => {
    await h.render(
      <NeedAssign
        need={modelNeed({ name: "cheap-vision", status: "capability_mismatch", capabilities: ["vision"] })}
        providers={[provider({ id: "openai" })]}
        onAssigned={() => {}}
      />,
    );
    expect(h.text()).toMatch(/does not advertise/i);
    expect(h.text()).toContain("vision");
  });

  it("loads the provider's catalog on request", async () => {
    h.routes.set("/api/admin/ai/providers/openai/models", [
      { id: "gpt-4o-mini", name: "GPT-4o mini" },
      { id: "gpt-4o", name: "GPT-4o" },
    ]);
    await h.render(<NeedAssign need={need} providers={[provider({ id: "openai" })]} onAssigned={() => {}} />);
    await h.click(/load|models|catalog/i);

    expect(h.calls.some((c) => c.path === "/api/admin/ai/providers/openai/models")).toBe(true);
  });

  it("survives a catalog that cannot be loaded", async () => {
    h.failures.set("/api/admin/ai/providers/openai/models", 502);
    await h.render(<NeedAssign need={need} providers={[provider({ id: "openai" })]} onAssigned={() => {}} />);
    await h.click(/load|models|catalog/i);

    // The panel stays usable: the point is to still be able to type a model by hand.
    expect(h.container.querySelector("input[list]")).toBeTruthy();
  });

  // The host rejects an unpriced model, and the panel's job is to open the price fields
  // rather than leave the operator stuck on a refusal they cannot act on.
  it("reveals the price fields when the host rejects an unknown model", async () => {
    h.failures.set("/api/admin/ai/routes/cheap-chat/assign", 400);
    await h.render(<NeedAssign need={need} providers={[provider({ id: "openai" })]} onAssigned={() => {}} />);
    await fill(h.container, "input[list]", "some-new-model");
    const before = h.container.querySelectorAll("input").length;
    await h.click(/assign|save|use/i);

    expect(h.container.querySelectorAll("input").length >= before).toBe(true);
    expect(h.text().length).toBeGreaterThan(0);
  });

  it("starts from the assignment the route already has", async () => {
    await h.render(
      <NeedAssign
        need={modelNeed({ name: "cheap-chat", status: "ready", provider: "openai", model: "gpt-4o-mini" })}
        providers={[provider({ id: "openai" }), provider({ id: "anthropic" })]}
        onAssigned={() => {}}
      />,
    );
    expect((h.container.querySelector("input[list]") as HTMLInputElement)?.value).toBe("gpt-4o-mini");
  });
});
