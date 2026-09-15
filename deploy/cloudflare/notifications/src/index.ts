import webpush from "web-push";

interface Env {
  SUBSCRIPTIONS: DurableObjectNamespace;
  CC_SHARED_TOKEN: string;
  VAPID_PUBLIC_KEY: string;
  VAPID_PRIVATE_KEY: string;
  VAPID_SUBJECT: string;
}

type Subscription = {
  endpoint: string;
  expirationTime: number | null;
  keys: {
    p256dh: string;
    auth: string;
  };
};

type NotificationPayload = {
  id: string;
  idempotencyKey: string;
  title: string;
  body: string;
  url?: string;
  subject?: string;
  collapsed?: number;
};

const SUBSCRIPTION_PREFIX = "subscription:";
const MAX_BODY_BYTES = 64 * 1024;
const MAX_ENDPOINT_BYTES = 4096;

class WorkerInputError extends Error {}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = parseURL(request.url);
    if (url === null) return json({ error: "Invalid request URL" }, 400);

    if (request.method === "GET" && url.pathname === "/vapid-public-key") {
      if (!env.VAPID_PUBLIC_KEY)
        return json({ error: "Worker is not configured" }, 500);
      return json({ publicKey: env.VAPID_PUBLIC_KEY });
    }

    if (!authorized(request, env)) return json({ error: "Unauthorized" }, 401);

    try {
      if (request.method === "POST" && url.pathname === "/subscriptions") {
        const subscription = await readJSON<Subscription>(request);
        const error = validateSubscription(subscription);
        if (error) return json({ error }, 400);
        return subscriptions(env).fetch(
          internalRequest("/subscriptions", "POST", subscription),
        );
      }
      if (
        request.method === "POST" &&
        url.pathname === "/subscriptions/remove"
      ) {
        const payload = await readJSON<{ endpoint: string }>(request);
        if (
          !payload ||
          typeof payload.endpoint !== "string" ||
          !validEndpoint(payload.endpoint)
        ) {
          return json({ error: "Invalid endpoint" }, 400);
        }
        return subscriptions(env).fetch(
          internalRequest("/subscriptions/remove", "POST", payload),
        );
      }
      if (request.method === "POST" && url.pathname === "/notify") {
        const payload = await readJSON<NotificationPayload>(request);
        const error = validateNotification(payload);
        if (error) return json({ error }, 400);
        return deliver(env, payload);
      }
      return json({ error: "Not found" }, 404);
    } catch (error) {
      if (error instanceof WorkerInputError)
        return json({ error: error.message }, 400);
      console.error("notification worker request failed", error);
      return json({ error: "Request failed" }, 500);
    }
  },
} satisfies ExportedHandler<Env>;

export class PushSubscriptions {
  constructor(private readonly state: DurableObjectState) {}

  async fetch(request: Request): Promise<Response> {
    const url = parseURL(request.url);
    if (url === null) return json({ error: "Invalid request URL" }, 400);
    if (request.method === "POST" && url.pathname === "/subscriptions") {
      const subscription = await request.json<Subscription>();
      await this.state.storage.put(
        SUBSCRIPTION_PREFIX + subscription.endpoint,
        subscription,
      );
      return json({ ok: true });
    }
    if (request.method === "POST" && url.pathname === "/subscriptions/remove") {
      const payload = await request.json<{ endpoint: string }>();
      await this.state.storage.delete(SUBSCRIPTION_PREFIX + payload.endpoint);
      return json({ ok: true });
    }
    if (request.method === "GET" && url.pathname === "/subscriptions") {
      const values = await this.state.storage.list<Subscription>({
        prefix: SUBSCRIPTION_PREFIX,
      });
      return json([...values.values()]);
    }
    return json({ error: "Not found" }, 404);
  }
}

function subscriptions(env: Env): DurableObjectStub {
  const id = env.SUBSCRIPTIONS.idFromName("control-center");
  return env.SUBSCRIPTIONS.get(id);
}

async function deliver(
  env: Env,
  payload: NotificationPayload,
): Promise<Response> {
  const response = await subscriptions(env).fetch(
    internalRequest("/subscriptions", "GET"),
  );
  if (!response.ok) return json({ error: "Could not load subscriptions" }, 502);
  const list = await response.json<Subscription[]>();
  if (list.length === 0) return json({ ok: true, delivered: 0 });

  webpush.setVapidDetails(
    env.VAPID_SUBJECT,
    env.VAPID_PUBLIC_KEY,
    env.VAPID_PRIVATE_KEY,
  );
  const dead: string[] = [];
  let failures = 0;
  for (const subscription of list) {
    try {
      await webpush.sendNotification(
        subscription,
        JSON.stringify({
          title: payload.title,
          body: payload.body,
          url: safeURL(payload.url),
          tag: payload.idempotencyKey || payload.id,
        }),
      );
    } catch (error) {
      const status = statusCode(error);
      if (status === 404 || status === 410) dead.push(subscription.endpoint);
      else failures++;
    }
  }

  for (const endpoint of dead) {
    await subscriptions(env).fetch(
      internalRequest("/subscriptions/remove", "POST", { endpoint }),
    );
  }
  if (failures > 0)
    return json({ error: "One or more push deliveries failed" }, 502);
  return json({ ok: true, delivered: list.length - dead.length });
}

function authorized(request: Request, env: Env): boolean {
  const token = env.CC_SHARED_TOKEN;
  return (
    typeof token === "string" &&
    token.length > 0 &&
    request.headers.get("Authorization") === `Bearer ${token}`
  );
}

function internalRequest(
  path: string,
  method: string,
  body?: unknown,
): Request {
  return new Request(`https://subscriptions.internal${path}`, {
    method,
    headers:
      body === undefined ? undefined : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function readJSON<T>(request: Request): Promise<T> {
  const length = request.headers.get("content-length");
  if (length !== null && Number(length) > MAX_BODY_BYTES)
    throw new WorkerInputError("body too large");
  const text = await request.text();
  if (new TextEncoder().encode(text).byteLength > MAX_BODY_BYTES)
    throw new WorkerInputError("body too large");
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new WorkerInputError("invalid JSON body");
  }
}

function validateSubscription(value: Subscription): string | null {
  if (!value || typeof value !== "object") return "Invalid subscription";
  if (!validEndpoint(value.endpoint)) return "Invalid endpoint";
  if (
    !value.keys ||
    !validBase64URL(value.keys.p256dh) ||
    !validBase64URL(value.keys.auth)
  ) {
    return "Invalid subscription keys";
  }
  return null;
}

function validEndpoint(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    new TextEncoder().encode(value).byteLength > MAX_ENDPOINT_BYTES
  )
    return false;
  const url = parseURL(value);
  return (
    url !== null &&
    url.protocol === "https:" &&
    url.hostname !== "" &&
    !url.username &&
    !url.password &&
    !url.hash
  );
}

function parseURL(value: string): URL | null {
  try {
    return new URL(value);
  } catch {
    return null;
  }
}

function validBase64URL(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    /^[A-Za-z0-9_-]+$/.test(value)
  );
}

function validateNotification(value: NotificationPayload): string | null {
  if (!value || typeof value !== "object") return "Invalid notification";
  if (typeof value.id !== "string" || value.id.length > 256)
    return "Invalid notification id";
  if (
    typeof value.idempotencyKey !== "string" ||
    value.idempotencyKey.length > 256
  )
    return "Invalid idempotency key";
  if (typeof value.title !== "string" || value.title.length > 1024)
    return "Invalid notification title";
  if (typeof value.body !== "string" || value.body.length > 8192)
    return "Invalid notification body";
  if (value.url !== undefined && typeof value.url !== "string")
    return "Invalid notification URL";
  return null;
}

function safeURL(value: string | undefined): string {
  return value !== undefined && value.startsWith("/") && !value.startsWith("//")
    ? value
    : "/";
}

function statusCode(error: unknown): number {
  if (typeof error === "object" && error !== null && "statusCode" in error) {
    const value = (error as { statusCode?: unknown }).statusCode;
    return typeof value === "number" ? value : 0;
  }
  return 0;
}

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}
