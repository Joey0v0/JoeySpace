'use strict';

const fs = require('node:fs');
const path = require('node:path');

const profile = process.env.CODEX_BROWSER_PROFILE;
const pageURL = process.env.CODEX_BROWSER_URL;
const token = process.env.CODEX_BROWSER_TOKEN;
if (!profile || !pageURL || !token) throw new Error('browser check requires an isolated profile, URL and token');

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(label, check, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const result = await check();
    if (result) return result;
    await sleep(100);
  }
  throw new Error(`${label} timed out`);
}

async function main() {
  const port = await until('Chrome DevTools port', () => {
    const file = path.join(profile, 'DevToolsActivePort');
    return fs.existsSync(file) ? Number(fs.readFileSync(file, 'utf8').split(/\r?\n/)[0]) : 0;
  });
  const page = await until('chat page', async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/list`);
      if (!response.ok) return null;
      return (await response.json()).find(item => item.type === 'page' && item.url === pageURL);
    } catch { return null; }
  });
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
  try {
    await until('chat page scripts', () => evaluate("document.readyState === 'complete' && typeof directUnreadPage !== 'undefined'"));
    await evaluate(`(() => {
      const token = document.getElementById('token');
      token.value = ${JSON.stringify(token)};
      token.dispatchEvent(new Event('input', { bubbles: true }));
      const peer = document.getElementById('toUserId');
      peer.value = '43';
      peer.dispatchEvent(new Event('input', { bubbles: true }));
    })()`);
    if (!await evaluate("!document.getElementById('btnDirectUnreadRefresh').disabled")) throw new Error('unread button did not enable');
    await evaluate("document.getElementById('btnDirectUnreadRefresh').click()");
    await until('initial unread', () => evaluate("document.getElementById('directUnreadCount').textContent === '1'"));
    await evaluate("document.getElementById('btnDirectLatest').click()");
    await until('history rows', () => evaluate("document.getElementById('directHistoryList').children.length === 3"));
    const ids = await evaluate("Array.from(document.getElementById('directHistoryList').children, row => row.dataset.messageId)");
    if (JSON.stringify(ids) !== JSON.stringify(['9007199254740999', '9007199254740998', '9007199254740997'])) throw new Error('browser history IDs or order mismatch');
    await evaluate("document.getElementById('btnDirectUnreadRefresh').click()");
    await until('unread after history', () => evaluate("document.getElementById('directUnreadCount').textContent === '1'"));
    if (!await evaluate("!document.getElementById('btnDirectReadLoaded').disabled")) throw new Error('explicit read button did not enable');
    await evaluate("document.getElementById('btnDirectReadLoaded').click()");
    await until('unread after explicit read', () => evaluate("document.getElementById('directUnreadCount').textContent === '0'"));
    if (!await evaluate("document.getElementById('btnDirectReadLoaded').disabled")) throw new Error('read button remained enabled after confirmation');
    process.stdout.write('PASS: real Chrome chat page loaded history, kept unread after viewing, and marked loaded incoming messages explicitly.\n');
  } finally {
    try { await command('Browser.close'); } catch { socket.close(); }
  }
}

main().catch(error => { process.stderr.write(`${error.message}\n`); process.exitCode = 1; });
