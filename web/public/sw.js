// Service worker for the scanner (scope /scan). It keeps the app shell
// available offline. API traffic is never cached here: the offline command
// queue and task cache live in IndexedDB (src/lib/offline.ts).
const SHELL = "cx-shell-v1";

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(["/scan", "/icon.svg", "/manifest.webmanifest"])).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin || url.pathname.startsWith("/api/")) return;

  // Pages under /scan: network first, fall back to the cached shell.
  if (req.mode === "navigate" && url.pathname.startsWith("/scan")) {
    e.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          if (res.ok) caches.open(SHELL).then((c) => c.put("/scan", copy));
          return res;
        })
        .catch(() => caches.match("/scan")),
    );
    return;
  }
  // Built assets are content-hashed: cache first.
  if (url.pathname.startsWith("/_next/static/") || url.pathname === "/icon.svg") {
    e.respondWith(
      caches.match(req).then((hit) => hit || fetch(req).then((res) => {
        const copy = res.clone();
        if (res.ok) caches.open(SHELL).then((c) => c.put(req, copy));
        return res;
      })),
    );
  }
});
