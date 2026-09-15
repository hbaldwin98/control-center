import { describe, expect, it, vi } from "vitest";
import { CloudflarePushSetup } from "./PushSetup";
import { setupHarness } from "./testing/harness";

const h = setupHarness();

describe("CloudflarePushSetup", () => {
  it("re-registers an existing browser subscription when settings opens", async () => {
    const subscription = {
      toJSON: () => ({
        endpoint: "https://push.example.test/send",
        expirationTime: null,
        keys: { p256dh: "AQ", auth: "Ag" },
      }),
    };
    const registration = {
      pushManager: {
        getSubscription: vi.fn().mockResolvedValue(subscription),
      },
    };
    const serviceWorker = {
      register: vi.fn().mockResolvedValue(registration),
      ready: Promise.resolve(registration),
    };
    const original = Object.getOwnPropertyDescriptor(
      navigator,
      "serviceWorker",
    );
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: serviceWorker,
    });
    vi.stubGlobal("PushManager", class PushManager {});
    vi.stubGlobal("Notification", { permission: "granted" });

    try {
      h.routes.set(
        "/api/admin/notifications/channels/cloudflare/push-subscriptions",
        {},
      );
      await h.render(<CloudflarePushSetup channelId="cloudflare" />);

      const call = h.calls.find(
        (entry) =>
          entry.method === "POST" &&
          entry.path.endsWith("/cloudflare/push-subscriptions"),
      );
      expect(call?.body).toEqual({
        endpoint: "https://push.example.test/send",
        expirationTime: null,
        keys: { p256dh: "AQ", auth: "Ag" },
      });
    } finally {
      if (original) Object.defineProperty(navigator, "serviceWorker", original);
      else Reflect.deleteProperty(navigator, "serviceWorker");
    }
  });
});
