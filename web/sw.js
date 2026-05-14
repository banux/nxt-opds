// The literal token below is replaced by the server at request time with the
// running binary version (handleSW). Each release therefore gets its own cache
// namespace; the activate handler deletes any cache whose name doesn't match.
const CACHE_NAME = 'nxt-opds-static-' + '__APP_VERSION__';
// EPUB downloads live in a separate cache so the static cache can be wiped
// on a UI release without losing the user's offline books.
const EPUB_CACHE = 'nxt-opds-epubs-v1';

const STATIC_ASSETS = [
  '/',
  '/manifest.json',
  '/favicon.svg',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
  'https://cdn.jsdelivr.net/npm/vue@3/dist/vue.global.prod.js',
  'https://cdn.tailwindcss.com',
  'https://cdn.jsdelivr.net/npm/jszip@3.10.1/dist/jszip.min.js',
  'https://cdn.jsdelivr.net/npm/epubjs@0.3.93/dist/epub.min.js',
];

// Install: cache static assets
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(STATIC_ASSETS.map(url => {
        return new Request(url, { mode: url.startsWith('http') ? 'cors' : 'same-origin' });
      })).catch(() => {
        // If CDN assets fail, still install
        return cache.addAll(['/', '/manifest.json', '/favicon.svg']);
      });
    })
  );
  self.skipWaiting();
});

// Activate: keep the EPUB cache around, but drop any stale static caches.
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => {
      return Promise.all(
        keys
          .filter((key) => key !== CACHE_NAME && key !== EPUB_CACHE)
          .map((key) => caches.delete(key))
      );
    })
  );
  self.clients.claim();
});

// Allow the page to ask us to pre-cache a book without opening the reader,
// e.g. "save for offline" UX: postMessage({type:'cacheEpub', url:'...'}).
self.addEventListener('message', (event) => {
  if (!event.data || event.data.type !== 'cacheEpub' || !event.data.url) return;
  event.waitUntil(
    caches.open(EPUB_CACHE).then((cache) =>
      fetch(event.data.url, { credentials: 'same-origin' }).then((resp) => {
        if (resp.ok) return cache.put(event.data.url, resp.clone());
      })
    )
  );
});

// Fetch: network-first for API, cache-first for static assets, cache-first
// (with opportunistic backfill) for EPUB downloads so the integrated reader
// works offline once a book has been opened at least once.
self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);

  // Never intercept non-GET requests
  if (event.request.method !== 'GET') return;

  // EPUB downloads → cache-first.  Stale-while-revalidate would re-download
  // every visit; the file is content-addressed by book id and rarely changes,
  // so prefer the cached copy and only hit the network on cache miss.
  if (/^\/opds\/books\/[^\/]+\/download$/.test(url.pathname)) {
    event.respondWith(
      caches.open(EPUB_CACHE).then((cache) =>
        cache.match(event.request).then((cached) => {
          if (cached) return cached;
          return fetch(event.request).then((resp) => {
            if (resp.ok) {
              // Clone before caching: the body stream can be read once.
              cache.put(event.request, resp.clone());
            }
            return resp;
          });
        })
      )
    );
    return;
  }

  // API and other OPDS calls: network-only (always fresh data).
  if (url.pathname.startsWith('/api/') ||
      url.pathname.startsWith('/opds/') ||
      url.pathname.startsWith('/mcp')) {
    return;
  }

  // Covers: network-first with cache fallback
  if (url.pathname.startsWith('/covers/')) {
    event.respondWith(
      fetch(event.request)
        .then((response) => {
          if (response.ok) {
            const clone = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone));
          }
          return response;
        })
        .catch(() => caches.match(event.request))
    );
    return;
  }

  // Static assets + CDN: cache-first
  event.respondWith(
    caches.match(event.request).then((cached) => {
      if (cached) return cached;
      return fetch(event.request).then((response) => {
        if (response.ok) {
          const clone = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone));
        }
        return response;
      });
    })
  );
});
