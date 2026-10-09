const test = require('node:test');
const assert = require('node:assert/strict');
const { checkHttpRoutes } = require('./verify-frontend-routes.cjs');

const html = () => new Response('<!doctype html><html><body>JoeySpace</body></html>', {
  status: 200,
  headers: { 'content-type': 'text/html; charset=utf-8' },
});
const jsonError = (status) => new Response('{"error":"unauthorized"}', {
  status,
  headers: { 'content-type': 'application/json' },
});

function proxy(overrides = {}) {
  return async (url) => {
    const pathname = new URL(url).pathname;
    if (pathname in overrides) return overrides[pathname];
    if (pathname.startsWith('/assets/')) return new Response(null, { status: 404 });
    if (pathname.startsWith('/api/') || pathname === '/ws-ticket') return jsonError(401);
    return html();
  };
}

test('accepts deep links and preserves missing asset and auth errors', async () => {
  const result = await checkHttpRoutes('http://127.0.0.1:18083', proxy());
  assert.equal(result.ok, true);
  assert.equal(result.checks.length, 5);
  assert.ok(result.checks.every((check) => check.ok));
});

test('rejects SPA fallback for a missing asset', async () => {
  const result = await checkHttpRoutes('http://127.0.0.1:18083', proxy({
    '/assets/f6-missing.js': html(),
  }));
  assert.equal(result.ok, false);
  assert.equal(result.checks.find((check) => check.path === '/assets/f6-missing.js').ok, false);
});

test('rejects SPA fallback for API and WebSocket ticket errors', async () => {
  const result = await checkHttpRoutes('http://127.0.0.1:18083', proxy({
    '/api/v1/user/info': html(),
    '/ws-ticket': html(),
  }));
  assert.equal(result.ok, false);
  assert.equal(result.checks.filter((check) => !check.ok).length, 2);
});

test('rejects an API error returned as HTML', async () => {
  const result = await checkHttpRoutes('http://127.0.0.1:18083', proxy({
    '/api/v1/user/info': new Response('<h1>Unauthorized</h1>', {
      status: 401,
      headers: { 'content-type': 'text/html' },
    }),
  }));
  assert.equal(result.ok, false);
});
