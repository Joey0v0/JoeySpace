'use strict';

// Vue F6 smoke check. Node 22+ and Chrome/Edge; no npm dependencies.
// Creates disposable account/team/group/task records, which are retained for review.
// Run only against an environment approved for test writes. This is not full F6 acceptance.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { randomBytes } = require('node:crypto');

class CheckError extends Error {
  constructor(message, blocked = false) { super(message); this.blocked = blocked; }
}
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, timeout = 20000, timeoutError = new CheckError('expected browser state was not observed before timeout')) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check()) return;
    await sleep(150);
  }
  throw timeoutError;
}
function chromeBinary() {
  if (process.env.JOEY_CHROME_PATH) return fs.existsSync(process.env.JOEY_CHROME_PATH) ? process.env.JOEY_CHROME_PATH : null;
  return ['C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].find(value => fs.existsSync(value));
}
async function connectPage(profile, child) {
  let port;
  await until(() => {
    if (child.exitCode !== null) throw new CheckError('browser exited before DevTools became available', true);
    const file = path.join(profile, 'DevToolsActivePort');
    if (!fs.existsSync(file)) return false;
    port = Number(fs.readFileSync(file, 'utf8').split(/\r?\n/)[0]);
    return Number.isInteger(port) && port > 0;
  }, 20000, new CheckError('Chrome DevTools port unavailable before startup timeout', true));
  let target;
  await until(async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/list`, { signal: AbortSignal.timeout(2000) });
      target = (await response.json()).find(item => item.type === 'page');
      return !!target;
    } catch { return false; }
  }, 20000, new CheckError('Chrome DevTools page target unavailable before startup timeout', true));
  const socket = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new CheckError('DevTools connection timeout', true)), 10000);
    socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, { once: true });
    socket.addEventListener('error', () => { clearTimeout(timer); reject(new CheckError('DevTools connection unavailable', true)); }, { once: true });
  });
  let sequence = 0;
  const pending = new Map(), listeners = new Set();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (!message.id) { for (const listener of listeners) listener(message.method, message.params); return; }
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id); clearTimeout(waiter.timer);
    if (message.error) waiter.reject(new CheckError('DevTools command failed'));
    else waiter.resolve(message.result);
  });
  socket.addEventListener('close', () => {
    for (const waiter of pending.values()) { clearTimeout(waiter.timer); waiter.reject(new CheckError('DevTools connection closed')); }
    pending.clear();
  });
  function command(method, params = {}) {
    const id = ++sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new CheckError('DevTools command timeout')); }, 15000);
      pending.set(id, { resolve, reject, timer });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }
  async function evaluate(expression) {
    const result = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new CheckError('Vue page evaluation failed');
    return result.result.value;
  }
  return { command, evaluate, socket, listeners };
}

async function main() {
  let stage = 'prerequisites', page, child, profile;
  const completed = [], data = {};
  try {
    if (Number(process.versions.node.split('.')[0]) < 22 || typeof WebSocket !== 'function') throw new CheckError('Node 22+ with built-in WebSocket is required', true);
    let site;
    try { site = new URL(process.env.JOEY_F6_URL || 'http://127.0.0.1:18083'); }
    catch { throw new CheckError('JOEY_F6_URL must be a valid HTTP(S) site origin', true); }
    if (!['http:', 'https:'].includes(site.protocol) || site.username || site.password || site.search || site.hash || site.pathname !== '/') throw new CheckError('JOEY_F6_URL must contain only the HTTP(S) site origin', true);
    const origin = site.origin, wsOrigin = origin.replace(/^http/, 'ws');
    const chrome = chromeBinary();
    if (!chrome) throw new CheckError('Chrome/Edge unavailable; set JOEY_CHROME_PATH', true);
    async function api(method, endpoint, body, token, key) {
      let response;
      try {
        response = await fetch(origin + endpoint, { method,
          headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
            ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(key ? { 'Idempotency-Key': key } : {}) },
          body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15000), redirect: 'error' });
      } catch { throw new CheckError('same-origin service unreachable or request timed out', true); }
      if (!response.ok) throw new CheckError(`setup API returned HTTP ${response.status}`, [502, 503, 504].includes(response.status));
      let value;
      try { value = await response.json(); } catch { throw new CheckError('setup API did not return JSON'); }
      if (value.code !== 0) throw new CheckError('setup API returned a non-success code');
      return value.data || {};
    }
    const suffix = randomBytes(5).toString('hex'), password = randomBytes(24).toString('base64url');
    data.username = `f6_vue_${suffix}`;
    const groupName = `F6 Vue group ${suffix}`, taskTitle = `F6 Vue task ${suffix}`;
    stage = 'disposable-account';
    await api('POST', '/api/v1/user/register', { username: data.username, password, nickname: data.username });
    const token = (await api('POST', '/api/v1/user/login', { username: data.username, password })).token;
    if (typeof token !== 'string' || !token) throw new CheckError('setup login returned no token');
    const self = await api('GET', '/api/v1/user/info', undefined, token);
    const requireID = value => { if (typeof value !== 'string' || !/^[1-9]\d*$/.test(value)) throw new CheckError('setup API returned an invalid record ID'); return value; };
    const selfID = requireID(self.id);
    data.team = requireID((await api('POST', '/api/v1/teams', { name: `F6 Vue team ${suffix}` }, token)).team_id);
    data.group = requireID((await api('POST', `/api/v1/teams/${data.team}/groups`, { name: groupName }, token, `f6-group-${suffix}`)).group_id);
    await api('POST', `/api/v1/teams/${data.team}/groups/${data.group}/join`, undefined, token);
    data.task = requireID((await api('POST', `/api/v1/teams/${data.team}/tasks`, {
      title: taskTitle, description: 'Disposable F6 browser route check', assignee_id: selfID,
      source_group_id: '0', source_message_id: '0', due_at_unix_ms: 0,
    }, token, `f6-task-${suffix}`)).task_id);
    completed.push(stage);

    stage = 'chrome-start';
    profile = fs.mkdtempSync(path.join(os.tmpdir(), 'joeyspace-f6-vue-'));
    child = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--window-size=1440,900', 'about:blank'],
    { stdio: 'ignore', windowsHide: true });
    let launchFailed = false;
    child.on('error', () => { launchFailed = true; });
    await sleep(100);
    if (launchFailed) throw new CheckError('browser could not start', true);
    page = await connectPage(profile, child);
    const requests = new Map(), sockets = new Map();
    let originViolation = false;
    page.listeners.add((method, params) => {
      if (method === 'Network.requestWillBeSent') {
        const url = new URL(params.request.url);
        if (['Fetch', 'XHR'].includes(params.type) || url.pathname.startsWith('/api/v1/') || url.pathname === '/ws-ticket') {
          if (url.origin !== origin) originViolation = true;
          requests.set(params.requestId, { path: url.pathname, status: 0 });
        }
      } else if (method === 'Network.responseReceived' && requests.has(params.requestId)) {
        const url = new URL(params.response.url);
        if (url.origin !== origin) originViolation = true;
        requests.get(params.requestId).status = params.response.status;
      } else if (method === 'Network.webSocketCreated') {
        const url = new URL(params.url);
        if (url.origin !== wsOrigin || url.pathname !== '/ws') originViolation = true;
        sockets.set(params.requestId, { status: 0, origin: '' });
      } else if (method === 'Network.webSocketWillSendHandshakeRequest' && sockets.has(params.requestId)) {
        const headers = params.request.headers;
        const handshakeOrigin = Object.entries(headers).find(([key]) => key.toLowerCase() === 'origin')?.[1] || '';
        if (handshakeOrigin !== origin) originViolation = true;
        sockets.get(params.requestId).origin = handshakeOrigin;
      } else if (method === 'Network.webSocketHandshakeResponseReceived' && sockets.has(params.requestId)) {
        sockets.get(params.requestId).status = params.response.status;
      }
    });
    await page.command('Network.enable');
    await page.command('Page.enable');
    const successful = endpoint => [...requests.values()].some(item => item.path === endpoint && item.status === 200);
    async function rendered(route, expression) {
      await until(() => page.evaluate(`location.pathname === ${JSON.stringify(route)} && (${expression})`));
    }
    async function navigate(route) {
      const result = await page.command('Page.navigate', { url: origin + route });
      if (result.errorText) throw new CheckError('Vue document navigation failed');
    }
    stage = 'vue-login';
    await navigate('/login');
    await rendered('/login', "document.querySelector('h1')?.textContent === '登录协作空间'");
    await page.evaluate(`(() => {
      for (const [name, value] of ${JSON.stringify([['username', data.username], ['password', password]])}) {
        const field = document.querySelector('input[name="' + name + '"]');
        if (!field) throw new Error('missing login field');
        field.value = value; field.dispatchEvent(new Event('input', { bubbles: true }));
      }
      document.querySelector('form').requestSubmit();
    })()`);
    await rendered('/messages', `document.querySelector('.sample-ribbon')?.textContent.includes(${JSON.stringify(data.username)})`);
    if (!successful('/api/v1/user/login') || !successful('/api/v1/user/info')) throw new CheckError('browser login/profile API success not observed');
    completed.push(stage);

    stage = 'messages-refresh';
    const previous = requests.size;
    await page.command('Page.reload', { ignoreCache: true });
    await rendered('/messages', `document.querySelector('.sample-ribbon')?.textContent.includes(${JSON.stringify(data.username)})`);
    await until(() => [...requests.values()].slice(previous).some(item => item.path === '/api/v1/user/info' && item.status === 200));
    completed.push(stage);

    stage = 'group-deep-link';
    const groupRoute = `/messages/teams/${data.team}/groups/${data.group}`;
    await navigate(groupRoute);
    await rendered(groupRoute, `document.querySelector('.chat-heading h2')?.textContent === ${JSON.stringify(groupName)} && document.querySelector('.chat-connection')?.textContent === '实时连接正常'`);
    await until(() => successful(`/api/v1/teams/${data.team}/groups/${data.group}`) && successful('/ws-ticket') && [...sockets.values()].some(item => item.status === 101 && item.origin === origin));
    const groupRefreshStart = requests.size;
    await page.command('Page.reload', { ignoreCache: true });
    await until(() => [...requests.values()].slice(groupRefreshStart).some(item => item.path === '/api/v1/teams/' + data.team + '/groups/' + data.group && item.status === 200));
    await rendered(groupRoute, `document.querySelector('.chat-heading h2')?.textContent === ${JSON.stringify(groupName)}`);
    completed.push(stage);

    stage = 'task-deep-link';
    const taskRoute = `/tasks/teams/${data.team}/${data.task}`;
    await navigate(taskRoute);
    await rendered(taskRoute, `document.querySelector('[aria-label="任务详情"] h2')?.textContent === ${JSON.stringify(taskTitle)}`);
    await until(() => successful(`/api/v1/teams/${data.team}/tasks/${data.task}`));
    const taskRefreshStart = requests.size;
    await page.command('Page.reload', { ignoreCache: true });
    await until(() => [...requests.values()].slice(taskRefreshStart).some(item => item.path === '/api/v1/teams/' + data.team + '/tasks/' + data.task && item.status === 200));
    await rendered(taskRoute, `document.querySelector('[aria-label="任务详情"] h2')?.textContent === ${JSON.stringify(taskTitle)}`);
    completed.push(stage);

    stage = 'same-origin-api-ws';
    if (originViolation) throw new CheckError('browser API or WebSocket traffic left the configured site origin');
    if (![...sockets.values()].some(item => item.status === 101 && item.origin === origin)) throw new CheckError('same-origin WebSocket Upgrade with page Origin was not observed');
    completed.push(stage);
    process.stdout.write(`PASS: Vue smoke; ${completed.join('; ')}\n`);
    process.stdout.write(`EVIDENCE: ${requests.size} API/ticket requests; ${[...sockets.values()].filter(item => item.status === 101).length} WS upgrades; all observed API/WS origins matched\n`);
  } catch (error) {
    const blocked = error instanceof CheckError && error.blocked;
    process.stderr.write(`${blocked ? 'BLOCKED' : 'FAIL'} [${stage}]: ${error instanceof CheckError ? error.message : 'unexpected check failure (details suppressed to protect credentials)'}\n`);
    process.stdout.write(`COMPLETED: ${completed.join('; ') || 'none'}\n`);
    process.exitCode = blocked ? 2 : 1;
  } finally {
    if (data.username) process.stdout.write(`TEST DATA (retained): user ${data.username}; team ${data.team || 'not created'}; group ${data.group || 'not created'}; task ${data.task || 'not created'}\n`);
    if (page) { try { await page.command('Browser.close'); } catch { /* browser may have exited */ } page.socket.close(); }
    if (child && child.exitCode === null) {
      child.kill();
      await Promise.race([new Promise(resolve => child.once('exit', resolve)), sleep(2000)]);
    }
    if (profile) {
      const target = path.resolve(profile), temporaryRoot = path.resolve(os.tmpdir());
      if (target.startsWith(temporaryRoot + path.sep) && path.basename(target).startsWith('joeyspace-f6-vue-')) {
        try { fs.rmSync(target, { recursive: true, force: true }); } catch { /* Chrome may still hold its disposable profile */ }
      }
    }
  }
}
main().catch(() => { process.stderr.write('FAIL [cleanup]: cleanup failed (details suppressed)\n'); process.exitCode = 1; });
