const VERSION = {{.Version}};
const SHELL_CACHE = `shell-${VERSION}`;
const LIB_CACHE = "libs-v1";
const PRECACHE = {{.Precache}};
const OFFLINE_URL = "/offline";
const LIB_HOSTS = ["unpkg.com"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE)
      .then((cache) => cache.addAll(PRECACHE))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith("shell-") && k !== SHELL_CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

async function navigate(request) {
  const url = new URL(request.url);
  try {
    const response = await fetch(request);
    if (url.pathname === "/" && response.ok) {
      const cache = await caches.open(SHELL_CACHE);
      cache.put("/", response.clone());
    }
    return response;
  } catch {
    const cache = await caches.open(SHELL_CACHE);
    if (url.pathname === "/") {
      const home = await cache.match("/");
      if (home) return home;
    }
    return (await cache.match(OFFLINE_URL)) || Response.error();
  }
}

async function cacheFirst(request, cacheName) {
  const cache = await caches.open(cacheName);
  const hit = await cache.match(request);
  if (hit) return hit;
  const response = await fetch(request);
  if (response.ok) cache.put(request, response.clone());
  return response;
}

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") return;
  const url = new URL(request.url);

  if (request.mode === "navigate") {
    event.respondWith(navigate(request));
    return;
  }
  if (url.origin === self.location.origin && url.pathname.startsWith("/static/") && url.searchParams.has("v")) {
    event.respondWith(cacheFirst(request, SHELL_CACHE));
    return;
  }
  if (LIB_HOSTS.includes(url.hostname)) {
    event.respondWith(cacheFirst(request, LIB_CACHE));
  }
});
