'use strict';

const fs = require('node:fs');
const path = require('node:path');

const baseURL = process.env.FRONTEND_PREVIEW_URL;
const profile = process.env.FRONTEND_BROWSER_PROFILE;
if (!baseURL || !profile) {
  throw new Error('Set FRONTEND_PREVIEW_URL and FRONTEND_BROWSER_PROFILE');
}
const origin = new URL(baseURL).origin;
if (!/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(origin)) {
  throw new Error('Browser check accepts only a local preview URL');
}

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(label, predicate, timeout = 15000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await predicate();
    if (value) return value;
    await pause(100);
  }
  throw new Error(`Timed out: ${label}`);
}

async function connect() {
  const port = await until('DevTools port', () => {
    const file = path.join(profile, 'DevToolsActivePort');
    return fs.existsSync(file) ? Number(fs.readFileSync(file, 'utf8').split(/\r?\n/)[0]) : null;
  });
  const page = await until('preview tab', async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/list`);
      if (!response.ok) return null;
      return (await response.json()).find(item => item.type === 'page' && item.url.startsWith(origin));
    } catch { return null; }
  });
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });
  let nextID = 0;
  const pending = new Map();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (!pending.has(message.id)) return;
    const { resolve, reject } = pending.get(message.id);
    pending.delete(message.id);
    if (message.error) reject(new Error(message.error.message));
    else resolve(message.result);
  });
  function command(method, params = {}) {
    return new Promise((resolve, reject) => {
      const id = ++nextID;
      pending.set(id, { resolve, reject });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }
  async function evaluate(expression) {
    const result = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
    return result.result.value;
  }
  return { command, evaluate, socket };
}

async function main() {
  const browser = await connect();
  const { command, evaluate } = browser;
  const checks = [];
  try {
    await command('Page.enable');
    await command('Runtime.enable');
    await command('Page.navigate', { url: `${origin}/messages` });
    await until('unread overview', () => evaluate("document.body.textContent.includes('未读总览') && document.querySelector('.module-nav') !== null"));
    checks.push('默认总览');

    const initialUnread = await evaluate("document.querySelector('.small-count')?.textContent?.trim()");
    if (initialUnread !== '3') throw new Error(`Unexpected sample unread conversation count: ${initialUnread}`);
    const directHref = await evaluate("Array.from(document.querySelectorAll('a[href]')).map(a => a.getAttribute('href')).find(h => h?.includes('9007199254740993'))");
    if (!directHref) throw new Error('Large-ID direct conversation link is missing');
    await evaluate(`document.querySelector('a[href="${directHref}"]').click()`);
    await until('direct conversation', () => evaluate("location.pathname.endsWith('9007199254740993')"));
    if (!await evaluate("document.querySelector('.module-nav') !== null && document.body.textContent.includes('未读总览')")) {
      throw new Error('Conversation navigation hid persistent navigation or list');
    }
    checks.push('会话切换、大整数 ID 与侧栏保留');

    await command('Emulation.setDeviceMetricsOverride', { width: 1280, height: 720, deviceScaleFactor: 1, mobile: false });
    const composer = await evaluate(`(() => {
      const area = document.querySelector('.composer-area');
      const scroll = document.querySelector('.chat-scroll');
      if (!area || !scroll) return null;
      const rect = area.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom, scrollable: getComputedStyle(scroll).overflowY === 'auto' };
    })()`);
    if (!composer || composer.top < 0 || composer.bottom > 721 || !composer.scrollable) {
      throw new Error(`Conversation composer is clipped: ${JSON.stringify(composer)}`);
    }
    const conversationShot = await command('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
    fs.writeFileSync(path.join(profile, 'frontend-conversation-1280x720.png'), Buffer.from(conversationShot.data, 'base64'));
    checks.push('1280×720 输入区与消息滚动');

    await evaluate('history.back()');
    await until('browser back to overview', () => evaluate("location.pathname === '/messages'"));
    checks.push('浏览器返回');

    await evaluate(`Array.from(document.querySelectorAll('.filter-tabs button')).find(button => button.textContent.includes('@我'))?.click()`);
    await until('mention filter', () => evaluate("document.querySelectorAll('.unread-card').length === 1"));
    checks.push('@我筛选');

    await evaluate("Array.from(document.querySelectorAll('a[href]')).find(a => a.getAttribute('href') === '/messages' && a.textContent.includes('未读总览'))?.click()");
    await until('return to overview', () => evaluate("location.pathname === '/messages'"));
    if (!await evaluate("document.body.textContent.includes('未读总览')")) throw new Error('Overview did not return');
    checks.push('返回总览');

    await command('Page.navigate', { url: `${origin}/messages/direct/unknown-preview` });
    await until('unknown conversation', () => evaluate("document.body.textContent.includes('当前会话不可用')"));
    if (!await evaluate("document.querySelector('.module-nav') !== null")) throw new Error('Unknown conversation hid navigation');
    checks.push('未知会话说明');

    await command('Page.navigate', { url: `${origin}/messages` });
    await until('overview after unknown', () => evaluate("location.pathname === '/messages' && document.body.textContent.includes('未读总览')"));
    const searchResult = await evaluate(`(() => {
      const input = document.querySelector('input[aria-label="搜索会话"]');
      if (!input) return false;
      input.value = '不存在的样例会话';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    })()`);
    if (!searchResult) throw new Error('Conversation search input missing');
    await until('empty search state', () => evaluate("document.body.textContent.includes('没有匹配的会话')"));
    checks.push('搜索空状态');

    await command('Page.navigate', { url: `${origin}/messages` });
    await until('keyboard target page', () => evaluate("location.pathname === '/messages' && document.querySelector('.filter-tabs button') !== null"));
    await command('Page.bringToFront');
    const focused = [];
    for (let index = 0; index < 15; index += 1) {
      await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
      await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
      focused.push(await evaluate(`(() => {
        const active = document.activeElement;
        const style = getComputedStyle(active);
        return {
          tag: active?.tagName,
          text: active?.textContent?.trim().slice(0, 30),
          label: active?.getAttribute('aria-label'),
          href: active?.getAttribute('href'),
          className: active?.className,
          visibleFocus: parseFloat(style.outlineWidth) >= 2 || getComputedStyle(active.parentElement).boxShadow !== 'none',
        };
      })()`));
    }
    if (!focused.some(item => item.label === '搜索会话') || !focused.some(item => item.text?.includes('我的任务')) || !focused.some(item => item.text?.includes('@我')) || !focused.some(item => item.className?.includes('overview-entry')) || !focused.some(item => item.className?.includes('conversation-row'))) {
      throw new Error(`Keyboard focus missed a key control: ${JSON.stringify(focused)}`);
    }
    if (focused.some(item => (item.label === '搜索会话' || item.text?.includes('我的任务') || item.text?.includes('@我') || item.className?.includes('overview-entry') || item.className?.includes('conversation-row')) && !item.visibleFocus)) {
      throw new Error(`Keyboard focus is not visible on a key control: ${JSON.stringify(focused)}`);
    }
    await evaluate(`document.querySelector('a.conversation-row[href="${directHref}"]').focus()`);
    await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await until('keyboard conversation open', () => evaluate("location.pathname.endsWith('9007199254740993')"));
    await evaluate("document.querySelector('a.overview-entry').focus()");
    await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    await until('keyboard overview return', () => evaluate("location.pathname === '/messages'"));
    checks.push('键盘可达且激活会话与总览、焦点可见');

    await command('Page.navigate', { url: `${origin}/messages` });
    await until('overview reset', () => evaluate("location.pathname === '/messages' && document.body.textContent.includes('未读总览')"));
    const afterUnread = await evaluate("document.querySelector('.small-count')?.textContent?.trim()");
    if (afterUnread !== initialUnread) throw new Error(`Unread count changed after viewing sample: ${initialUnread} -> ${afterUnread}`);
    checks.push('阅读样例未清空未读');

    for (const [width, height] of [[1280, 720], [1440, 900], [1920, 1080]]) {
      await command('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
      const layout = await evaluate(`(() => ({
        pageWidth: document.documentElement.scrollWidth,
        viewportWidth: innerWidth,
        navVisible: document.querySelector('.module-nav')?.getBoundingClientRect().width > 0,
      }))()`);
      if (layout.pageWidth > layout.viewportWidth + 1 || !layout.navVisible) {
        throw new Error(`Desktop layout overflow at ${width}x${height}: ${JSON.stringify(layout)}`);
      }
      const shot = await command('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      fs.writeFileSync(path.join(profile, `frontend-${width}x${height}.png`), Buffer.from(shot.data, 'base64'));
      checks.push(`${width}×${height} 无水平溢出`);
    }
    process.stdout.write(`PASS: ${checks.join('；')}\n`);
  } finally {
    browser.socket.close();
  }
}

main().catch(error => {
  process.stderr.write(`${error.stack || error.message}\n`);
  process.exitCode = 1;
});
