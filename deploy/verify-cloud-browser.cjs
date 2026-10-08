'use strict';

// Real Chrome UI smoke check against a loopback Gateway/WS tunnel. Uses only
// built-in Node modules and disposable accounts; never prints credentials.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { randomBytes } = require('node:crypto');

const pageURL = process.env.JOEY_BROWSER_URL || 'http://127.0.0.1:18082/demo/chat';
const wsBase = process.env.JOEY_BROWSER_WS || 'ws://127.0.0.1:18081';
const gateway = new URL(pageURL).origin;
if (!['127.0.0.1', 'localhost'].includes(new URL(pageURL).hostname) ||
    !['127.0.0.1', 'localhost'].includes(new URL(wsBase).hostname)) {
  throw new Error('browser check requires loopback Gateway and WebSocket tunnel URLs');
}
if (typeof WebSocket !== 'function') throw new Error('Node 22+ with built-in WebSocket is required');

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(label, check, timeoutMs = 15000, intervalMs = 150) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const result = await check();
    if (result) return result;
    await sleep(intervalMs);
  }
  throw new Error(`${label} timed out`);
}

async function api(method, endpoint, body, token, key) {
  const headers = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (key) headers['Idempotency-Key'] = key;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(gateway + endpoint, {
    method, headers, body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15000)
  });
  const value = await response.json();
  if (response.status !== 200 || value.code !== 0) {
    throw new Error(`${method} ${endpoint} failed: HTTP ${response.status}, code ${value.code}`);
  }
  return value.data || {};
}

function chromeBinary() {
  const candidates = [process.env.JOEY_CHROME_PATH,
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    '/usr/bin/google-chrome', '/usr/bin/chromium'];
  return candidates.find(candidate => candidate && fs.existsSync(candidate));
}

async function connectPage(profile) {
  const port = await until('Chrome DevTools port', () => {
    const file = path.join(profile, 'DevToolsActivePort');
    return fs.existsSync(file) ? Number(fs.readFileSync(file, 'utf8').split(/\r?\n/)[0]) : 0;
  }, 20000);
  const page = await until('cloud chat page', async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/list`, { signal: AbortSignal.timeout(2000) });
      if (!response.ok) return null;
      return (await response.json()).find(item => item.type === 'page' && item.url === pageURL);
    } catch { return null; }
  }, 20000);
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', () => reject(new Error('Chrome DevTools connection failed')), { once: true });
  });
  let sequence = 0;
  const pending = new Map();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (!pending.has(message.id)) return;
    const { resolve, reject } = pending.get(message.id);
    pending.delete(message.id);
    if (message.error) reject(new Error(`Chrome DevTools ${message.error.code}`));
    else resolve(message.result);
  });
  function command(method, params = {}) {
    const id = ++sequence;
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }
  async function evaluate(expression) {
    const answer = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (answer.exceptionDetails) throw new Error('chat page script evaluation failed');
    return answer.result.value;
  }
  return { socket, command, evaluate };
}

async function main() {
  const suffix = randomBytes(5).toString('hex');
  const names = [`js_browser_a_${suffix}`, `js_browser_b_${suffix}`];
  const password = randomBytes(24).toString('base64url');
  const chrome = chromeBinary();
  if (!chrome) throw new Error('Chrome or Edge is required');
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'joeyspace-browser-'));
  let processHandle;
  let page;
  let peer;
  const completed = [];
  const failures = [];
  let team, group;
  try {
    await api('POST', '/api/v1/user/register', { username: names[0], password, nickname: names[0] });
    await api('POST', '/api/v1/user/register', { username: names[1], password, nickname: names[1] });
    const peerToken = (await api('POST', '/api/v1/user/login', { username: names[1], password })).token;
    const peerID = (await api('GET', '/api/v1/user/info', undefined, peerToken)).id;

    processHandle = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--window-size=1280,900', pageURL],
    { stdio: 'ignore', windowsHide: true });
    page = await connectPage(profile);
    const { evaluate } = page;
    async function fill(id, value) {
      await evaluate(`(() => { const field = document.getElementById(${JSON.stringify(id)});
        field.value = ${JSON.stringify(value)};
        field.dispatchEvent(new Event('input', { bubbles: true }));
        field.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    }
    async function click(selector) {
      await evaluate(`(() => { const button = document.querySelector(${JSON.stringify(selector)});
        if (!button || button.disabled) throw new Error('button unavailable'); button.click(); return true; })()`);
    }
    await until('chat page scripts', () => evaluate("document.readyState === 'complete' && typeof doLogin === 'function'"));
    await fill('username', names[0]);
    await fill('password', password);
    await click('#btnLogin');
    await until('browser login', () => evaluate("document.getElementById('loginStatus').textContent.includes('Login success')"));
    const token = await evaluate("document.getElementById('token').value");
    if (!token) throw new Error('login did not fill the token field');
    const selfID = (await api('GET', '/api/v1/user/info', undefined, token)).id;
    completed.push('Chrome login and rendered account state');

    team = (await api('POST', '/api/v1/teams', { name: `browser-${suffix}` }, token)).team_id;
    await api('POST', `/api/v1/teams/${team}/members`, { user_id: peerID }, token);
    group = (await api('POST', `/api/v1/teams/${team}/groups`, { name: `browser-${suffix}` }, token,
      `group-${suffix}`)).group_id;
    await api('POST', `/api/v1/teams/${team}/groups/${group}/join`, undefined, peerToken);
    await fill('teamId', team);
    await fill('wsUrl', wsBase);
    await click('#btnConnect');
    await until('browser WebSocket', () => evaluate("document.getElementById('wsStatus').textContent === 'Connected'"));
    completed.push('Chrome WebSocket connection');

    const peerMessages = [];
    peer = new WebSocket(`${wsBase}/ws?token=${encodeURIComponent(peerToken)}`);
    peer.addEventListener('message', event => {
      try { peerMessages.push(JSON.parse(event.data)); } catch { /* unexpected frame is checked by timeout */ }
    });
    await until('peer WebSocket', () => peer.readyState === WebSocket.OPEN);
    await sleep(500);

    const directText = `browser-direct-${suffix}`;
    await fill('toUserId', peerID);
    await fill('chatType', '1');
    await fill('msgContent', directText);
    await click('.send-panel button');
    await until('browser direct send', () => peerMessages.some(message =>
      message.type === 'chat' && message.data?.content === directText), 20000);
    const inboundID = `browser-inbound-${suffix}`;
    peer.send(JSON.stringify({ type: 'chat', data: { msg_id: inboundID, to_id: selfID,
      chat_type: 1, content_type: 1, content: inboundID } }));
    await until('browser direct receive', () => evaluate(`document.getElementById('logPanel').textContent.includes(${JSON.stringify(inboundID)})`), 20000);
    completed.push('Chrome direct chat send and receive');

    const taskTitle = `Browser task ${suffix}`;
    await fill('taskTitle', taskTitle);
    await fill('taskAssigneeID', peerID);
    await click('button[onclick="doCreateTask()"]');
    await until('browser task card', () => evaluate(`document.getElementById('taskList').textContent.includes(${JSON.stringify(taskTitle)})`), 20000);
    completed.push('Chrome task creation and list rendering');

    const groupText = `browser-group-${suffix}`;
    await fill('toUserId', group);
    await fill('chatType', '2');
    await fill('msgContent', groupText);
    await click('.send-panel button');
    await until('browser group send', () => peerMessages.some(message =>
      message.type === 'chat' && message.data?.content === groupText), 20000);
    completed.push('Chrome group chat send and peer delivery');

    try {
    await fill('aiQuestion', '请用一句话介绍这个测试群的用途。');
    let answer;
    let aiRetried = false;
    const failedDurations = [];
    for (let attempt = 0; attempt < 2; attempt++) {
      const started = Date.now();
      await click('#btnAskAI');
      try {
        answer = await until('browser AI answer', async () => {
          const value = await evaluate("document.getElementById('aiAnswer').textContent");
          if (value.startsWith('Ask failed:')) throw new Error('AI answer failed in browser: ' + value);
          return value && value !== 'Asking...' ? value : null;
        }, 45000);
        break;
      } catch (error) {
        failedDurations.push(Date.now() - started);
        if (attempt !== 0 || !/AI HTTP 50[34]/.test(error.message)) {
          throw new Error(`${error.message}; failed request durations: ${failedDurations.join(',')} ms`);
        }
        aiRetried = true;
        await sleep(3000);
      }
    }
    if (!answer.trim()) throw new Error('browser AI answer is empty');
    completed.push('Chrome AI question and answer rendering' + (aiRetried ? ' (after one HTTP 503/504 retry)' : ''));
    } catch (error) {
      failures.push('Chrome AI question and answer: ' + error.message);
    }

    try {
    const triggerText = '@AI 整理任务 请创建一项标题为浏览器验收草稿的测试任务，不指定负责人和截止时间。';
    await fill('msgContent', triggerText);
    await click('.send-panel button');
    const triggerMessage = await until('browser @AI group delivery', () => peerMessages.find(message =>
      message.type === 'chat' && message.data?.content === triggerText && message.data?.id), 20000);
    const sourceID = triggerMessage.data.id;
    await until('background @AI draft', async () => {
      const response = await fetch(`${gateway}/api/v1/teams/${team}/groups/${group}/agent-triggers/${sourceID}`,
        { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(12000) });
      if (response.status === 404) return false;
      if (!response.ok) throw new Error(`@AI trigger status HTTP ${response.status}`);
      const value = await response.json();
      if (value.code !== 0) throw new Error('@AI trigger status failed');
      if (value.data?.status === 'exhausted') throw new Error('@AI model budget exhausted');
      return value.data?.status === 'completed';
    }, 240000, 1000);
    await evaluate('doRefreshTeamGroupHistory()');
    await until('browser @AI review button', () => evaluate(`Array.from(document.querySelectorAll('#logPanel .log-entry')).some(entry =>
      entry.textContent.includes(${JSON.stringify(triggerText)}) &&
      Array.from(entry.querySelectorAll('button')).some(button => button.textContent === '查看 AI 草稿'))`), 15000);
    await evaluate(`(() => { const entry = Array.from(document.querySelectorAll('#logPanel .log-entry')).find(row =>
      row.textContent.includes(${JSON.stringify(triggerText)}) &&
      Array.from(row.querySelectorAll('button')).some(button => button.textContent === '查看 AI 草稿'));
      Array.from(entry.querySelectorAll('button')).find(button => button.textContent === '查看 AI 草稿').click(); })()`);
    await until('browser draft review card', () => evaluate("document.querySelector('#multiDraftItems article[data-index=\"0\"]') !== null"), 20000);
    const confirmReady = await evaluate("!document.querySelector('#multiDraftItems [data-action=\"confirm\"][data-index=\"0\"]').disabled");
    if (!confirmReady) throw new Error('AI draft requires manual review before confirmation');
    await click('#multiDraftItems [data-action="confirm"][data-index="0"]');
    await until('browser draft task confirmation', () => evaluate("document.querySelector('#multiDraftItems article[data-index=\"0\"]')?.textContent.includes('Task state: succeeded')"), 30000);
    await until('browser bot reply', () => peerMessages.some(message =>
      message.type === 'chat' && message.data?.sender_type === 2 &&
      String(message.data?.msg_id || '').startsWith('bot-task:')), 30000);
    completed.push('Chrome @AI draft review, task confirmation and bot reply');
    } catch (error) {
      failures.push('Chrome @AI draft and reply: ' + error.message);
    }

    process.stdout.write((failures.length ? 'PARTIAL: ' : 'PASS: ') + completed.join('; ') + '\n');
    if (failures.length) {
      for (const failure of failures) process.stderr.write('FAILED: ' + failure + '\n');
      process.exitCode = 1;
    }
    process.stdout.write(`TEST DATA: users ${names.join(', ')}; team ${team}; group ${group}\n`);
  } catch (error) {
    process.stderr.write('FAIL after: ' + (completed.join('; ') || 'no completed checks') + '\n');
    process.stderr.write('REASON: ' + error.message + '\n');
    process.stderr.write(`TEST DATA: users ${names.join(', ')}; team ${team || 'not created'}; group ${group || 'not created'}\n`);
    process.exitCode = 1;
  } finally {
    if (peer && peer.readyState === WebSocket.OPEN) peer.close();
    if (page) {
      try { await page.command('Browser.close'); } catch { page.socket.close(); }
    }
    if (processHandle && processHandle.exitCode === null) processHandle.kill();
    const target = path.resolve(profile), tmp = path.resolve(os.tmpdir());
    if (target.startsWith(tmp + path.sep) && path.basename(target).startsWith('joeyspace-browser-')) {
      try { fs.rmSync(target, { recursive: true, force: true }); } catch { /* Chrome may still be closing */ }
    }
  }
}

main().catch(error => { process.stderr.write(`NOT READY: ${error.message}\n`); process.exitCode = 2; });
