/**
 * Sub-path aware API URL construction — fixes GitHub issue #255
 * ("反代用域名访问打开商店空白").
 *
 * The store is frequently reverse-proxied under a prefix, e.g. Lucky serving it
 * at `https://domain.tld/store/`. Static assets already survived that (Vite is
 * configured with `base: './'`, so index.html emits relative `./assets/...`),
 * but every API request was root-absolute: a bare `/api/apps` request resolved
 * to `https://domain.tld/api/apps`, outside the proxy's location block, and
 * 404s. The first failed request left the SPA with no data — a blank page —
 * while direct LAN-IP access (mounted at `/`) kept working.
 *
 * The prefix cannot be baked in at build time: users mount the store at
 * arbitrary paths. It is therefore derived at runtime from the one thing that
 * always knows where the app really lives — the URL the JS bundle itself was
 * served from.
 */

/**
 * Join a mount point and an API path into one absolute, single-slashed path.
 *
 * Pure by construction: `basePath` is a PARAMETER, never read from
 * `import.meta`, `location` or any other ambient global, so every proxy layout
 * is testable without a browser, a bundler or a proxy (see base.test.ts).
 *
 * @param basePath Mount point, with or without leading/trailing slashes
 *                 (`'/'`, `''`, `'/store'`, `'/store/'`, `'/a/b/'`).
 * @param path     API path, with or without a leading slash
 *                 (`'/api/apps'`, `'api/apps'`); a query string is allowed.
 */
export function resolveApiUrl(basePath: string, path: string): string {
  // Split the query off first: it may legitimately contain encoded characters
  // that must survive verbatim, so only the path portion gets normalised.
  const queryStart = path.indexOf('?');
  const rawPath = queryStart === -1 ? path : path.slice(0, queryStart);
  const query = queryStart === -1 ? '' : path.slice(queryStart);

  // Collapse duplicate slashes and drop trailing ones: '//store//' -> '/store'.
  const collapsed = basePath.replace(/\/{2,}/g, '/').replace(/\/+$/, '');
  // A base that lost (or never had) its leading slash must regain one, else the
  // result would be relative and resolve against the current route instead.
  const prefix = collapsed === '' || collapsed.startsWith('/') ? collapsed : `/${collapsed}`;

  const suffix = rawPath.replace(/\/{2,}/g, '/').replace(/^\/+/, '');

  // `prefix` never ends in a slash and `suffix` never starts with one, so
  // exactly one separator is emitted. A leading '//' would be parsed as a
  // protocol-relative URL and send the request to a foreign host.
  return `${prefix}/${suffix}${query}`;
}

let cachedBasePath: string | undefined;

/**
 * The path prefix the app is actually mounted at, derived at runtime.
 *
 * Production: the bundle is emitted into `assets/`, so the directory one level
 * above its own module URL is the mount point — `/store/assets/index-a1b2.js`
 * yields `/store/`, and a plain root deployment yields `/`.
 *
 * Development: Vite serves modules from `/src/...` and proxies `/api` to the Go
 * backend (vite.config.ts), so the bundle-relative derivation does not apply.
 */
export function apiBasePath(): string {
  if (cachedBasePath === undefined) {
    // @vite-ignore keeps Vite's asset-URL plugin from trying to resolve '..'
    // to a file at build time; the expression must survive into the bundle so
    // it evaluates against wherever the proxy actually served the chunk from.
    cachedBasePath = import.meta.env.DEV
      ? '/'
      : new URL(/* @vite-ignore */ '..', import.meta.url).pathname;
  }
  return cachedBasePath;
}

/** `apiUrl('/api/apps')` -> `/api/apps` at root, `/store/api/apps` behind a proxy. */
export function apiUrl(path: string): string {
  return resolveApiUrl(apiBasePath(), path);
}
