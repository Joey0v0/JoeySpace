const ROUTES = [
  { path: '/messages/teams/2/groups/3', kind: 'page' },
  { path: '/tasks/teams/2/3', kind: 'page' },
  { path: '/assets/f6-missing.js', kind: 'missing-asset' },
  { path: '/api/v1/user/info', kind: 'unauthenticated' },
  { path: '/ws-ticket', kind: 'unauthenticated' },
];

async function checkHttpRoutes(baseUrl, fetchImpl = fetch) {
  const origin = new URL(baseUrl);
  if (!['http:', 'https:'].includes(origin.protocol) || origin.username || origin.password) {
    throw new Error('JOEY_F6_URL must be an HTTP(S) URL without credentials');
  }
  const checks = [];
  for (const { path, kind } of ROUTES) {
    try {
      const response = await fetchImpl(new URL(path, origin).href, { redirect: 'manual' });
      const contentType = response.headers.get('content-type') || '';
      const isHtml = /(?:^|\s|;)text\/html(?:\s|;|$)/i.test(contentType);
      const ok = kind === 'page'
        ? response.status === 200 && isHtml
        : kind === 'missing-asset'
          ? response.status === 404
          : response.status !== 200 && !isHtml;
      checks.push({ path, ok, status: response.status, contentType });
    } catch (error) {
      checks.push({ path, ok: false, error: error instanceof Error ? error.name : 'NetworkError' });
    }
  }
  return { ok: checks.every((check) => check.ok), checks };
}

module.exports = { checkHttpRoutes };

if (require.main === module) {
  checkHttpRoutes(process.env.JOEY_F6_URL || 'http://127.0.0.1:18083')
    .then(({ ok, checks }) => {
      for (const check of checks) {
        const detail = check.error || `${check.status} ${check.contentType || '(no content-type)'}`;
        console.log(`${check.ok ? 'PASS' : 'FAIL'} ${check.path}: ${detail}`);
      }
      process.exitCode = ok ? 0 : 1;
    })
    .catch((error) => {
      console.error(`FAIL route checker: ${error instanceof Error ? error.message : 'unknown error'}`);
      process.exitCode = 1;
    });
}
