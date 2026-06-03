// UBAG dashboard service worker.
// Strategy:
//   - Navigations (HTML): NETWORK-FIRST so a new deploy is always picked up.
//     Falls back to cached shell only when offline.
//   - Hashed build assets (/dashboard/_app/immutable/): CACHE-FIRST (safe — the
//     content hash changes on every build, so a stale entry can never shadow a
//     new build).
//   - Everything else (API /v1/*, etc.): pass through to the network untouched.
const CACHE = 'ubag-dashboard-v3';

self.addEventListener('install', (e) => {
  // Activate immediately; do not pre-cache the HTML shell (avoids serving a
  // stale index.html that points at old asset hashes).
  e.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Never intercept API calls — always go to the network.
  if (url.pathname.startsWith('/v1/')) return;

  // Cache-first for immutable hashed assets.
  if (url.pathname.includes('/_app/immutable/')) {
    e.respondWith(
      caches.match(req).then(
        (cached) =>
          cached ??
          fetch(req).then((res) => {
            const copy = res.clone();
            caches.open(CACHE).then((c) => c.put(req, copy)).catch(() => {});
            return res;
          }),
      ),
    );
    return;
  }

  // Network-first for navigations / HTML so new deploys are always picked up.
  const isNavigation = req.mode === 'navigate' || (req.headers.get('accept') ?? '').includes('text/html');
  if (isNavigation) {
    e.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(req, copy)).catch(() => {});
          return res;
        })
        .catch(() => caches.match(req).then((cached) => cached ?? caches.match('/dashboard/'))),
    );
  }
});
