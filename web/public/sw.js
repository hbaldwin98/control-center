/* Control Center's root-scoped Web Push service worker. */
self.addEventListener("push", (event) => {
  if (!event.data) return;

  let payload;
  try {
    payload = event.data.json();
  } catch {
    payload = { body: event.data.text() };
  }
  const title =
    typeof payload.title === "string" && payload.title
      ? payload.title
      : "Control Center";
  const body = typeof payload.body === "string" ? payload.body : "";
  const rawURL = typeof payload.url === "string" ? payload.url : "/";
  const url = rawURL.startsWith("/") && !rawURL.startsWith("//") ? rawURL : "/";

  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      tag: typeof payload.tag === "string" ? payload.tag : undefined,
      data: { url },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const rawURL = event.notification.data && event.notification.data.url;
  const url =
    typeof rawURL === "string" &&
    rawURL.startsWith("/") &&
    !rawURL.startsWith("//")
      ? rawURL
      : "/";

  event.waitUntil(
    self.clients
      .matchAll({ type: "window", includeUncontrolled: true })
      .then((clients) => {
        for (const client of clients) {
          if ("focus" in client) {
            return client.navigate(url).then((focused) => focused?.focus());
          }
        }
        return self.clients.openWindow(url);
      }),
  );
});
