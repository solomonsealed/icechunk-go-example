// Entry point of the Worker. Files in public/ are served without reaching
// it; /api/* comes here. It starts the Go WebAssembly module on the first
// request (Workers forbid the randomness and timers the Go runtime needs at
// startup in global scope), keeps what Go fetches from S3 in Cloudflare's
// cache, and caches whole API responses.
import "./build/wasm_exec.js";
import goModule from "./build/app.wasm";

const BUCKET = "ctrees-agb-100m-global";

// Every object in an Icechunk repository but one is immutable: chunks,
// manifests and snapshots are written once, under new names. Cloudflare may
// keep those for a year. The repo info file, `repo`, is replaced by every
// commit, so it is kept for a minute. Go calls globalThis.fetch, so this
// wrapper sees all of its requests.
const nativeFetch = globalThis.fetch;
globalThis.fetch = (input, init) => {
  const url = typeof input === "string" ? input : input.url;
  if (url.includes(BUCKET)) {
    const mutable = new URL(url).pathname.endsWith("/repo");
    const cacheTtlByStatus = mutable
      ? { "200-299": 60, "300-599": 0 }
      : { "200-299": 31536000, "403-404": 60, "500-599": 0 };
    init = { ...init, cf: { cacheEverything: true, cacheTtlByStatus } };
  }
  return nativeFetch(input, init);
};

let ready;

function startGo() {
  if (!ready) {
    ready = (async () => {
      const go = new Go();
      const instance = await WebAssembly.instantiate(goModule, go.importObject);
      // Runs main() until it blocks inside cfworker.Serve.
      go.run(instance);
      if (typeof globalThis.__goWorkerFetch !== "function") {
        throw new Error("the Go program did not call cfworker.Serve");
      }
    })();
    ready.catch(() => {
      ready = undefined;
    });
  }
  return ready;
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/api/")) {
      return new Response("Not found", { status: 404 });
    }
    // The Cache API is keyed by URL; the page asks for snapshot ids, so a
    // cached response is never stale.
    const cache = caches.default;
    let response = await cache.match(request);
    if (!response) {
      await startGo();
      response = await globalThis.__goWorkerFetch(request, env, ctx);
      if (response.status === 200 && request.method === "GET") {
        ctx.waitUntil(cache.put(request, response.clone()));
      }
    }
    return compressed(response);
  },
};

// Rasters shrink by a fifth to a half with gzip, but Cloudflare compresses
// only text types by itself; the header makes the runtime do it. It is added
// after caching: a cached response that already says gzip would be encoded
// twice.
function compressed(response) {
  if (response.headers.get("content-type") !== "application/octet-stream") return response;
  const headers = new Headers(response.headers);
  headers.set("content-encoding", "gzip");
  return new Response(response.body, { status: response.status, headers });
}
