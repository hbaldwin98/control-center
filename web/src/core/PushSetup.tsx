import { useEffect, useState } from "react";
import { ApiError, Button, Callout, Hint, Row, api } from "@cc/ui";

/**
 * Enrolls this browser with a core Cloudflare notification channel. The Worker keeps
 * the subscription and handles Web Push encryption; the app only forwards it through
 * the authenticated core API so the Worker credential never reaches the browser.
 */
export function CloudflarePushSetup({ channelId }: { channelId: string }) {
  const [supported, setSupported] = useState<boolean | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const inspect = async () => {
      if (
        !("serviceWorker" in navigator) ||
        !("PushManager" in window) ||
        !("Notification" in window)
      ) {
        if (!cancelled) setSupported(false);
        return;
      }
      try {
        const registration = await navigator.serviceWorker.register("/sw.js");
        const subscription = await registration.pushManager.getSubscription();
        if (!cancelled) {
          setSupported(true);
          setEnabled(subscription !== null);
        }
      } catch (err) {
        if (!cancelled) {
          setSupported(false);
          setError(formatErr(err));
        }
      }
    };
    void inspect();
    return () => {
      cancelled = true;
    };
  }, []);

  const enable = async () => {
    setBusy(true);
    setError(null);
    try {
      if (supported !== true)
        throw new Error(
          "This browser does not support phone push notifications.",
        );
      const permission =
        Notification.permission === "granted"
          ? "granted"
          : await Notification.requestPermission();
      if (permission !== "granted")
        throw new Error("Notification permission was not granted.");

      const key = await api.get<{ publicKey: string }>(
        pushPath(channelId, "push-key"),
      );
      const registration = await navigator.serviceWorker.ready;
      let subscription = await registration.pushManager.getSubscription();
      if (subscription === null) {
        subscription = await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: base64UrlToBytes(key.publicKey) as BufferSource,
        });
      }
      const json = subscription.toJSON();
      const keys = json.keys;
      if (!json.endpoint || !keys?.p256dh || !keys.auth) {
        throw new Error(
          "The browser returned an incomplete push subscription.",
        );
      }
      await api.post(pushPath(channelId, "push-subscriptions"), {
        endpoint: json.endpoint,
        expirationTime: json.expirationTime ?? null,
        keys: { p256dh: keys.p256dh, auth: keys.auth },
      });
      setEnabled(true);
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  const disable = async () => {
    setBusy(true);
    setError(null);
    try {
      const registration = await navigator.serviceWorker.ready;
      const subscription = await registration.pushManager.getSubscription();
      if (subscription !== null) {
        const endpoint = subscription.endpoint;
        await api.post(pushPath(channelId, "push-subscriptions/remove"), {
          endpoint,
        });
        await subscription.unsubscribe();
      }
      setEnabled(false);
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="cc-subsection">
      <div className="cc-group__title">Phone push on this device</div>
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {supported === false ? (
        <Hint>
          Phone push is unavailable in this browser or the service worker could
          not start.
        </Hint>
      ) : (
        <>
          <Hint>
            {enabled
              ? "This device is enrolled for native notifications."
              : "Enable native notifications for this device."}{" "}
            On iPhone, add Control Center to the Home Screen before enabling
            push.
          </Hint>
          <Row>
            <Button
              size="sm"
              variant={enabled ? "default" : "primary"}
              disabled={busy || supported === null}
              onClick={() => void (enabled ? disable() : enable())}
            >
              {busy
                ? "Working…"
                : enabled
                  ? "Disable on this device"
                  : "Enable phone push"}
            </Button>
          </Row>
        </>
      )}
    </div>
  );
}

function pushPath(channelId: string, suffix: string): string {
  return `/api/admin/notifications/channels/${encodeURIComponent(channelId)}/${suffix}`;
}

function base64UrlToBytes(value: string): Uint8Array {
  const padded = value + "=".repeat((4 - (value.length % 4)) % 4);
  const binary = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function formatErr(err: unknown): string {
  return err instanceof ApiError
    ? err.message
    : err instanceof Error
      ? err.message
      : String(err);
}
