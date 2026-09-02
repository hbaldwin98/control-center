/**
 * Shared mounting harness for screen tests.
 *
 * The screens under test are not pure: each one loads a snapshot on mount, most open a
 * live stream, and several read the router. Testing them therefore means standing up
 * enough of the browser that the wiring runs for real -- what a unit test of a helper
 * cannot see is exactly the wiring.
 *
 * It lives under `core/` deliberately. A plugin must stay standalone -- it may import
 * `@cc/ui` and its own directory and nothing else -- so a plugin's screen tests bring
 * their own harness rather than depending on this one. `boundary.test.ts` enforces that.
 * The duplication is the point: it is what keeps a plugin liftable.
 */
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";

/** One recorded request, so a test can assert what a screen asked the server to do. */
export type Call = { method: string; path: string; body: unknown };

export class FakeEventSource {
  static opened: string[] = [];
  static instances: FakeEventSource[] = [];
  readyState = 1;
  closed = false;
  onerror: ((event: Event) => void) | null = null;
  #listeners = new Map<string, Set<(event: { data: string }) => void>>();

  constructor(url: string) {
    FakeEventSource.opened.push(url);
    FakeEventSource.instances.push(this);
  }
  close(): void {
    this.closed = true;
  }
  addEventListener(type: string, fn: (event: { data: string }) => void): void {
    const set = this.#listeners.get(type) ?? new Set();
    set.add(fn);
    this.#listeners.set(type, set);
  }
  removeEventListener(type: string, fn: (event: { data: string }) => void): void {
    this.#listeners.get(type)?.delete(fn);
  }
  emit(type: string, topic = "", data: unknown = {}): void {
    const event = { data: JSON.stringify({ topic, data }) };
    for (const fn of this.#listeners.get(type) ?? []) fn(event);
  }
}

export type Harness = {
  container: HTMLDivElement;
  /** Responses by path, without query string. Mutate between renders to change them. */
  routes: Map<string, unknown>;
  /** Paths that must answer with an error status, so failure branches can be reached. */
  failures: Map<string, number>;
  /** Every request the screens made, in order. */
  calls: Call[];
  /** Mounts an element, then flushes the snapshot loads it started. */
  render: (el: ReactElement) => Promise<void>;
  /** Mounts an element under a router sitting at `path`. */
  renderAt: (path: string, el: ReactElement, routePath?: string) => Promise<void>;
  /** Flushes pending promises and effects -- use after clicking something. */
  settle: () => Promise<void>;
  /** Clicks the first element whose text matches, and settles. Returns false if absent. */
  click: (text: string | RegExp) => Promise<boolean>;
  /** Every button/link label currently on screen, for diagnosing a missed match. */
  labels: () => string[];
  /**
   * Clicks a control inside the card that mentions `within`.
   *
   * These screens repeat the same labels -- every card has a Delete -- so addressing a
   * control by label alone hits whichever card happens to come first. Scoping to the
   * card that names the thing under test is what makes the assertion mean something.
   */
  clickIn: (within: string, text: string | RegExp) => Promise<boolean>;
  text: () => string;
};

/**
 * Installs the harness for one test file. Call at module scope inside `describe`;
 * it registers its own beforeEach/afterEach.
 */
export function setupHarness(): Harness {
  const h = {} as Harness;
  h.routes = new Map();
  h.failures = new Map();
  h.calls = [];

  let root: Root;

  beforeEach(() => {
    // React only suppresses its act() warning when the environment opts in.
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    h.calls = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string | URL | Request, init?: RequestInit) => {
        const raw = typeof input === "string" ? input : String((input as URL).toString?.() ?? input);
        const path = raw.split("?")[0] ?? "";
        const method = init?.method ?? "GET";
        let body: unknown = undefined;
        if (typeof init?.body === "string") {
          try {
            body = JSON.parse(init.body);
          } catch {
            body = init.body;
          }
        }
        h.calls.push({ method, path, body });

        const failure = h.failures.get(path);
        if (failure !== undefined) {
          return Promise.resolve(
            new Response(JSON.stringify({ error: "failed", message: "failed" }), {
              status: failure,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        const payload = h.routes.get(path);
        return Promise.resolve(
          new Response(JSON.stringify(payload ?? {}), {
            status: payload === undefined ? 404 : 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );
    FakeEventSource.opened = [];
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);

    h.container = document.createElement("div");
    document.body.appendChild(h.container);
    root = createRoot(h.container);
  });

  afterEach(() => {
    act(() => root.unmount());
    h.container.remove();
    h.failures.clear();
    vi.unstubAllGlobals();
  });

  h.settle = async () => {
    // Two ticks: one for the fetch to resolve, one for the state it sets to render.
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });
  };

  h.render = async (el) => {
    await act(async () => {
      root.render(<MemoryRouter>{el}</MemoryRouter>);
    });
    await h.settle();
  };

  h.renderAt = async (path, el, routePath) => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path={routePath ?? path} element={el} />
          </Routes>
        </MemoryRouter>,
      );
    });
    await h.settle();
  };

  h.labels = () =>
    [...h.container.querySelectorAll("button, a, summary, [role=button]")]
      .map((el) => (el.textContent ?? "").trim())
      .filter(Boolean);

  h.click = async (text) => {
    const match = (s: string) => (typeof text === "string" ? s.includes(text) : text.test(s));
    const el = [...h.container.querySelectorAll("button, a, summary, [role=button]")].find((n) =>
      match((n.textContent ?? "").trim()),
    );
    if (!el) return false;
    await act(async () => {
      // happy-dom does not run a form's submit algorithm off a submit button's click,
      // so a form's onSubmit would never fire and the test would silently assert on a
      // handler that never ran. Dispatch the submit the browser would have.
      const button = el as HTMLButtonElement;
      const form = button.form ?? button.closest("form");
      if (form && (button.type === "submit" || button.tagName === "BUTTON")) {
        button.click();
        if (button.type === "submit") {
          form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
        }
      } else {
        (el as HTMLElement).click();
      }
    });
    await h.settle();
    return true;
  };

  h.clickIn = async (within, text) => {
    const match = (s: string) => (typeof text === "string" ? s.includes(text) : text.test(s));
    // The smallest element that both mentions `within` and holds a control: walking
    // from the deepest match outward avoids scoping to the whole page.
    const candidates = [...h.container.querySelectorAll("*")].filter((el) =>
      (el.textContent ?? "").includes(within),
    );
    const scope = candidates.at(-1)?.closest("section, article, form, div") ?? null;
    for (const node of [scope, ...candidates.reverse()]) {
      if (!node) continue;
      const el = [...node.querySelectorAll("button, a, summary, [role=button]")].find((n) =>
        match((n.textContent ?? "").trim()),
      );
      if (!el) continue;
      await act(async () => {
        const button = el as HTMLButtonElement;
        const form = button.form ?? button.closest("form");
        button.click();
        if (form && button.type === "submit") {
          form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
        }
      });
      await h.settle();
      return true;
    }
    return false;
  };

  h.text = () => h.container.textContent ?? "";
  return h;
}

/**
 * Types a value into a field, firing the events React listens for.
 *
 * React installs its own value setter on the element, so assigning `.value` directly is
 * invisible to it; the native prototype setter plus a bubbling input event is what a real
 * keystroke looks like from React's side.
 *
 * `index` picks among matches -- a form usually has several inputs of the same type, and
 * addressing them positionally is less brittle than depending on a label's exact wording.
 */
export async function fill(container: HTMLElement, selector: string, value: string, index = 0) {
  const all = [...container.querySelectorAll(selector)];
  const el = all[index] as HTMLInputElement | HTMLSelectElement | undefined;
  if (!el) {
    throw new Error(`no field at index ${index} matched ${selector} (found ${all.length})`);
  }
  await act(async () => {
    const proto =
      el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
