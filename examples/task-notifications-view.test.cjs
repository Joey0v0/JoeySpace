const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
const scripts = ['task-notifications.js', 'task-notifications-view.js'];

function element(tag) {
  return { tagName: tag.toUpperCase(), value: '', textContent: '', disabled: false, style: {}, dataset: {}, children: [], listeners: {},
    appendChild(child) { this.children.push(child); return child; },
    replaceChildren(...children) { this.children = children; },
    setAttribute(name, value) { this[name] = value; },
    addEventListener(name, callback) { (this.listeners[name] ||= []).push(callback); } };
}

function boot(fetch, values = {}) {
  const fields = {}, inlineInputs = {};
  for (const match of html.matchAll(/<(\w+)[^>]*\bid="([^"]+)"[^>]*>/g)) {
    fields[match[2]] = element(match[1]);
    fields[match[2]].value = match[0].match(/\bvalue="([^"]*)"/)?.[1] || '';
    const handler = match[0].match(/\boninput="([^"]*)"/);
    if (handler) inlineInputs[match[2]] = handler[1];
  }
  fields.token.value = 'test-token'; fields.teamId.value = '200';
  for (const [id, value] of Object.entries(values)) fields[id].value = value;
  const context = { fetch, crypto: require('node:crypto').webcrypto,
    location: { protocol: 'http:', hostname: 'gateway.test' },
    document: { getElementById: id => fields[id], createElement: element },
    console, encodeURIComponent,
    WebSocket: class { constructor() { assert.fail('notifications must not open WebSocket'); } },
    setInterval: () => assert.fail('notifications must not poll'),
    setTimeout: () => assert.fail('notifications must not schedule automatic reads'),
    localStorage: { setItem: () => assert.fail('notifications must not persist Token') } };
  vm.createContext(context);
  vm.runInContext(html.match(/<script>([\s\S]*?)<\/script>/)[1], context);
  // Load the complete production script order to catch interference with the
  // existing multi-draft page, including its input and login invalidation hooks.
  for (const match of html.matchAll(/<script src="\/demo\/([^"]+)"/g)) {
    const script = match[1];
    vm.runInContext(fs.readFileSync(path.join(__dirname, script), 'utf8'), context, { filename: script });
  }
  function input(id, value) {
    fields[id].value = value;
    if (inlineInputs[id]) vm.runInContext(inlineInputs[id], context);
    for (const listener of fields[id].listeners.input || []) listener();
  }
  return { fields, context, input, page: context.taskNotificationsPage };
}

function response(items = [], cursor = '0', status = 200) {
  return { ok: status === 200, status, json: async () => ({ code: status === 200 ? 0 : 1000,
    msg: '<img src=x> private test-token Bearer secret', data: { notifications: items, next_before_notification_id: cursor } }) };
}
function notice(id = '9007199254741033', changes = {}) {
  return { notification_id: id, task_id: '9007199254740993', actor_id: '9007199254740995',
    from_status: 0, to_status: 1, created_at_unix_ms: Date.UTC(2026, 9, 5, 0, 0, 0), read_at_unix_ms: 0, ...changes };
}
function hint(id = '9007199254741099', teamID = '200') {
  return { type: 'task_notification_changed', data: { version: 1, notification_id: id, team_id: teamID } };
}
function twenty() { return Array.from({ length: 20 }, (_, index) => notice(String(9007199254741033n - BigInt(index)))); }
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function descendants(node) { return [node, ...node.children.flatMap(descendants)]; }
function text(node) { return descendants(node).map(child => child.textContent).join('\n'); }
function refresh(fields) { return fields.btnRefreshTaskNotifications.onclick(); }
function more(fields) { return fields.btnMoreTaskNotifications.onclick(); }
function assertCleared(fields) {
  assert.equal(fields.taskNotificationsList.children.length, 0);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
}

test('HTML provides a separate accessible panel and same-origin scripts in controller then view order', () => {
  for (const id of ['taskNotificationsPanel', 'btnRefreshTaskNotifications', 'btnMoreTaskNotifications',
    'taskNotificationsStatus', 'taskNotificationsRealtimeStatus', 'taskNotificationsList']) {
    assert.equal([...html.matchAll(new RegExp('id="' + id + '"', 'g'))].length, 1, id);
  }
  assert.ok(html.indexOf('id="taskList"') < html.indexOf('id="taskNotificationsPanel"'));
  assert.ok(html.indexOf('id="taskNotificationsPanel"') < html.indexOf('Step 6 — Ask AI'));
  assert.match(html, /id="taskNotificationsStatus" role="status" aria-live="polite"/);
  const urls = [...html.matchAll(/<script src="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(urls.filter(url => scripts.some(name => url === '/demo/' + name)), scripts.map(name => '/demo/' + name));
  assert.ok(urls.indexOf('/demo/multi-draft-view.js') < urls.indexOf('/demo/task-notifications.js'));
  const view = fs.readFileSync(path.join(__dirname, scripts[1]), 'utf8');
  assert.doesNotMatch(view, /\.innerHTML\s*=/);
  assert.doesNotMatch(view, /toUserId|chatType|teamGroupSelect/);
  assert.match(view, /addEventListener\('input'/);
});

test('starts idle without reads or WS and requires only Token plus canonical team ID', () => {
  const { fields, input } = boot(() => assert.fail('unexpected automatic read'));
  assert.match(fields.taskNotificationsStatus.textContent, /尚未读取/);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
  assertCleared(fields);
  for (const value of ['', '0', '0200', '+200', '9223372036854775808']) {
    input('teamId', value);
    assert.equal(fields.btnRefreshTaskNotifications.disabled, true, value);
  }
  input('teamId', '200'); input('token', ' ');
  assert.equal(fields.btnRefreshTaskNotifications.disabled, true);
  input('token', 'test-token');
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  assert.equal(fields.toUserId.value, '', 'group selection is unnecessary');
});

test('team ID surrounding whitespace is normalized consistently with existing task controls', async () => {
  const calls = [];
  const { fields, input } = boot(async url => { calls.push(url); return response(); });
  input('teamId', ' 200 ');
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  await refresh(fields);
  assert.equal(calls[0], '/api/v1/teams/200/task-notifications?limit=20&before_notification_id=0');
});

test('manual refresh displays exact large IDs, historical status and Shanghai time as text', async () => {
  const calls = [], { fields, page } = boot(async (url, options) => { calls.push([url, options]); return response([notice()]); });
  await refresh(fields);
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], '/api/v1/teams/200/task-notifications?limit=20&before_notification_id=0');
  assert.equal(calls[0][1].headers.Authorization, 'Bearer test-token');
  const content = text(fields.taskNotificationsList);
  assert.match(content, /任务 #9007199254740993/);
  assert.match(content, /操作者 #9007199254740995：待办 → 进行中/);
  assert.match(content, /Asia\/Shanghai/);
  assert.match(content, /2026[\/\-]10[\/\-]05.*08:00:00/);
  assert.equal(page.state.items[0].notification_id, '9007199254741033');
  assert.match(fields.taskNotificationsStatus.textContent, /没有更早通知/);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
  assert.equal(descendants(fields.taskNotificationsList).some(node => ['IMG', 'SCRIPT', 'SVG'].includes(node.tagName)), false);
});

test('empty result has its own state and refresh remains available', async () => {
  const { fields } = boot(async () => response());
  await refresh(fields);
  assert.match(fields.taskNotificationsStatus.textContent, /当前没有任务状态通知/);
  assertCleared(fields);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
});

test('paging uses exact cursor, appends older records and refresh replaces the list', async () => {
  const first = twenty(), cursor = first.at(-1).notification_id, calls = [];
  const { fields, page } = boot(async url => {
    calls.push(url);
    if (calls.length === 1) return response(first, cursor);
    if (calls.length === 2) return response([notice(String(BigInt(cursor) - 1n), { from_status: 1, to_status: 2 })]);
    return response([notice('9007199254741099')]);
  });
  await refresh(fields);
  assert.match(fields.taskNotificationsStatus.textContent, /可加载更早通知/);
  assert.equal(fields.btnMoreTaskNotifications.disabled, false);
  await more(fields);
  assert.equal(calls[1], '/api/v1/teams/200/task-notifications?limit=20&before_notification_id=' + cursor);
  assert.equal(fields.taskNotificationsList.children.length, 21);
  assert.match(text(fields.taskNotificationsList), /进行中 → 完成/);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
  await more(fields);
  assert.equal(calls.length, 2, 'finished page cannot dispatch a request');
  await refresh(fields);
  assert.equal(page.state.items.length, 1);
  assert.equal(page.state.items[0].notification_id, '9007199254741099');
});

test('loading disables both controls and direct repeated click cannot dispatch another request', async () => {
  const pending = deferred(); let calls = 0;
  const { fields } = boot(() => { calls++; return pending.promise; });
  const request = refresh(fields);
  assert.match(fields.taskNotificationsStatus.textContent, /正在读取/);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, true);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
  await refresh(fields); await more(fields);
  assert.equal(calls, 1);
  pending.resolve(response()); await request;
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
});

test('next-page network failure keeps shown records and original cursor for explicit retry', async () => {
  const first = twenty(), cursor = first.at(-1).notification_id; let count = 0;
  const { fields, page } = boot(async () => {
    if (++count === 1) return response(first, cursor);
    if (count === 2) throw new Error('private test-token Bearer secret');
    return response();
  });
  await refresh(fields); await more(fields);
  assert.equal(page.state.cursor, cursor);
  assert.equal(fields.taskNotificationsList.children.length, 20);
  assert.equal(fields.btnMoreTaskNotifications.disabled, false);
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /test-token|secret|Bearer/);
  await more(fields);
  assert.equal(fields.taskNotificationsList.children.length, 20);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
});

for (const status of [401, 403]) test('permission refusal ' + status + ' clears displayed records and requires refresh', async () => {
  let count = 0;
  const { fields, page } = boot(async () => ++count === 1 ? response(twenty(), twenty().at(-1).notification_id) : response([], '0', status));
  await refresh(fields); await more(fields);
  assertCleared(fields);
  assert.equal(page.state.loaded, false);
  assert.equal(page.state.cursor, '0');
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /private|test-token|Bearer|<img/);
});

for (const id of ['token', 'teamId']) test(id + ' input A to B to A invalidates old response and clears immediately', async () => {
  const pending = deferred(); let count = 0;
  const { fields, input, page } = boot(() => ++count === 1 ? pending.promise : Promise.resolve(response([notice('9007199254741111')])));
  const oldRequest = refresh(fields), original = fields[id].value;
  input(id, id === 'token' ? 'another-token' : '201');
  assertCleared(fields);
  input(id, original);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  await refresh(fields);
  pending.resolve(response([notice()])); await oldRequest;
  assert.equal(page.state.items[0].notification_id, '9007199254741111');
  assert.equal(fields.taskNotificationsList.children.length, 1);
});

test('stale failure and finally cannot affect a new scope request still loading', async () => {
  const old = deferred(), current = deferred(); let count = 0;
  const { fields, input } = boot(() => ++count === 1 ? old.promise : current.promise);
  const oldRequest = refresh(fields);
  input('teamId', '201'); const newRequest = refresh(fields);
  old.reject(new Error('private old request failed')); await oldRequest;
  assert.match(fields.taskNotificationsStatus.textContent, /正在读取/);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, true);
  assertCleared(fields);
  current.resolve(response()); await newRequest;
  assert.match(fields.taskNotificationsStatus.textContent, /当前没有/);
});

test('same-token successful program login resets pending notice request before its response', async () => {
  const old = deferred(), newRead = deferred(); let reads = 0;
  const { fields, context, page } = boot(async (url) => {
    if (url === '/api/v1/user/login') return { json: async () => ({ code: 0, data: { token: 'test-token' } }) };
    if (url === '/api/v1/user/info') return { ok: true, json: async () => ({ code: 0, data: { id: '7' } }) };
    assert.ok(url.includes('/task-notifications?'), url);
    return ++reads === 1 ? old.promise : newRead.promise;
  });
  fields.username.value = 'same-person'; fields.password.value = 'test-password';
  const oldRequest = refresh(fields);
  await context.doLogin();
  assert.equal(fields.token.value, 'test-token');
  assertCleared(fields);
  assert.equal(page.state.loaded, false);
  const currentRequest = refresh(fields);
  old.resolve(response([notice()])); await oldRequest;
  assert.match(fields.taskNotificationsStatus.textContent, /正在读取/);
  newRead.resolve(response([notice('9007199254741111')])); await currentRequest;
  assert.equal(page.state.items[0].notification_id, '9007199254741111');
});

test('invalid server IDs cannot create markup or partially display a rejected page', async () => {
  const { fields, page } = boot(async () => response([notice(), notice('9007199254741032', { task_id: '<img src=x onerror=alert(1)>' })]));
  await refresh(fields);
  assertCleared(fields);
  assert.equal(page.state.items.length, 0);
  assert.ok(fields.taskNotificationsStatus.textContent);
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /<img|alert\(|test-token/);
});

function readResponse(id, time = Date.UTC(2026, 9, 5, 0, 1, 0)) {
  return { ok: true, status: 200, json: async () => ({ code: 0, data: { notification_id: id, read_at_unix_ms: time } }) };
}
function readButton(fields, index = 0) {
  return descendants(fields.taskNotificationsList.children[index]).find(node => node.tagName === 'BUTTON');
}

test('read and unread rows show their own state and first Shanghai read time without automatic PUT', async () => {
  const items = [notice(), notice('9007199254741032', { read_at_unix_ms: Date.UTC(2026, 9, 5, 0, 1, 0) })], calls = [];
  const { fields } = boot(async (url, init) => { calls.push({ url, init }); return response(items); });
  await refresh(fields);
  assert.equal(calls.length, 1); assert.equal(calls[0].init.method, 'GET');
  assert.match(text(fields.taskNotificationsList.children[0]), /未读/);
  assert.equal(readButton(fields).disabled, false);
  assert.match(text(fields.taskNotificationsList.children[1]), /已读；首次确认时间（Asia\/Shanghai）/);
  assert.match(text(fields.taskNotificationsList.children[1]), /08:01:00/);
  assert.equal(readButton(fields, 1), undefined);
});

test('clicking one unread row sends its large ID only and keeps cursor plus all other rows unchanged', async () => {
  const items = twenty(), target = items[0].notification_id, calls = [];
  const { fields, page } = boot(async (url, init) => {
    calls.push({ url, init }); return init.method === 'PUT' ? readResponse(target) : response(items, items.at(-1).notification_id);
  });
  await refresh(fields); const cursor = page.state.cursor;
  await readButton(fields).onclick();
  assert.equal(calls[1].url, '/api/v1/teams/200/task-notifications/' + target + '/read');
  assert.deepEqual(JSON.parse(JSON.stringify(calls[1].init)), { method: 'PUT', headers: { Authorization: 'Bearer test-token' } });
  assert.equal(page.state.cursor, cursor);
  assert.equal(page.state.items.length, 20);
  assert.match(text(fields.taskNotificationsList.children[0]), /已读；首次确认时间/);
  assert.equal(readButton(fields), undefined);
  assert.match(text(fields.taskNotificationsList.children[1]), /未读/);
  assert.equal(page.state.items[1].read_at_unix_ms, 0);
});

test('confirmation disables all row buttons and paging, including clicks through a previously retained button', async () => {
  const pending = deferred(); let puts = 0;
  const { fields } = boot((_, init) => init.method === 'PUT'
    ? (puts++, pending.promise) : Promise.resolve(response(twenty(), twenty().at(-1).notification_id)));
  await refresh(fields); const retained = readButton(fields);
  const request = retained.onclick();
  assert.match(fields.taskNotificationsStatus.textContent, /确认已读/);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, true);
  assert.equal(fields.btnMoreTaskNotifications.disabled, true);
  assert.equal(descendants(fields.taskNotificationsList).filter(node => node.tagName === 'BUTTON').every(node => node.disabled), true);
  await retained.onclick(); await refresh(fields); await more(fields);
  assert.equal(puts, 1);
  pending.resolve(readResponse(notice().notification_id)); await request;
  assert.equal(readButton(fields), undefined);
  assert.equal(fields.btnMoreTaskNotifications.disabled, false);
});

test('failed confirmation displays uncertainty and explicit retry shows fixed server first-read time', async () => {
  let puts = 0;
  const { fields, page } = boot(async (_, init) => {
    if (init.method !== 'PUT') return response([notice()]);
    if (++puts === 1) throw new Error('private test-token response lost after saving');
    return readResponse(notice().notification_id);
  });
  await refresh(fields); await readButton(fields).onclick();
  assert.match(text(fields.taskNotificationsList), /未读/);
  assert.equal(page.state.items[0].read_at_unix_ms, 0);
  assert.equal(fields.taskNotificationsStatus.textContent, '未能确认，请刷新或重试');
  assert.equal(readButton(fields).disabled, false);
  await readButton(fields).onclick();
  assert.match(text(fields.taskNotificationsList), /08:01:00/);
  assert.equal(readButton(fields), undefined);
  assert.equal(puts, 2);
});

for (const status of [401, 403]) test('mark permission refusal ' + status + ' clears the panel immediately', async () => {
  const { fields, page } = boot(async (_, init) => init.method === 'PUT' ? response([], '0', status) : response([notice()]));
  await refresh(fields); await readButton(fields).onclick();
  assertCleared(fields); assert.equal(page.state.loaded, false);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, false);
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /private|test-token|Bearer|<img/);
});

for (const id of ['teamId', 'token']) test('retained row button cannot mark same ID after ' + id + ' changes A to B to A', async () => {
  let puts = 0;
  const { fields, input } = boot(async (_, init) => {
    if (init.method === 'PUT') { puts++; return readResponse(notice().notification_id); }
    return response([notice()]);
  });
  await refresh(fields); const retained = readButton(fields), original = fields[id].value;
  input(id, id === 'token' ? 'B' : '201'); await refresh(fields);
  await retained.onclick(); assert.equal(puts, 0, 'old button cannot mark matching new-scope ID');
  input(id, original); await refresh(fields);
  await retained.onclick(); assert.equal(puts, 0, 'return to original scope still requires a current button');
  await readButton(fields).onclick(); assert.equal(puts, 1);
});

test('retained button detects programmatic team change even without input event', async () => {
  const calls = [];
  const { fields, page } = boot(async (url, init) => { calls.push([url, init]); return response([notice()]); });
  await refresh(fields); const retained = readButton(fields);
  fields.teamId.value = '201'; await retained.onclick();
  assertCleared(fields); assert.equal(page.state.loaded, false); assert.equal(calls.length, 1);
});

test('stale confirmation response after team switch cannot mark its new-scope matching ID', async () => {
  const pending = deferred();
  const { fields, input, page } = boot((_, init) => init.method === 'PUT' ? pending.promise : Promise.resolve(response([notice()])));
  await refresh(fields); const request = readButton(fields).onclick();
  input('teamId', '201'); await refresh(fields);
  pending.resolve(readResponse(notice().notification_id)); await request;
  assert.equal(page.state.items[0].read_at_unix_ms, 0);
  assert.match(text(fields.taskNotificationsList), /未读/);
  assert.equal(readButton(fields).disabled, false);
});

test('same-token successful login invalidates retained row button and pending mark', async () => {
  const pending = deferred(); let puts = 0;
  const { fields, context, page } = boot(async (url, init) => {
    if (url === '/api/v1/user/login') return { json: async () => ({ code: 0, data: { token: 'test-token' } }) };
    if (url === '/api/v1/user/info') return { ok: true, json: async () => ({ code: 0, data: { id: '7' } }) };
    if (init.method === 'PUT') { puts++; return pending.promise; }
    return response([notice()]);
  });
  await refresh(fields); const retained = readButton(fields), request = retained.onclick();
  fields.username.value = 'same-person'; fields.password.value = 'test-password';
  await context.doLogin(); assertCleared(fields); await refresh(fields);
  await retained.onclick(); assert.equal(puts, 1);
  pending.resolve(readResponse(notice().notification_id)); await request;
  assert.equal(page.state.items[0].read_at_unix_ms, 0); assert.equal(readButton(fields).disabled, false);
});

test('missing read state from an old Gateway is rejected instead of displaying an actionable unread row', async () => {
  const old = notice(); delete old.read_at_unix_ms;
  const { fields, page } = boot(async () => response([old]));
  await refresh(fields); assertCleared(fields); assert.equal(page.state.loaded, false);
  assert.match(fields.taskNotificationsStatus.textContent, /通知读取失败/);
});

test('realtime display begins empty without connecting, polling or reading automatically', () => {
  let reads = 0;
  const { fields, page } = boot(async () => { reads++; return response(); });
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  assert.equal(fields.taskNotificationsStatus.textContent, '尚未读取通知，请点击刷新通知。');
  assert.equal(page.state.items.length, 0);
  assert.equal(reads, 0);
});

test('hint shows fixed text without changing rows or marking read; duplicate and listed IDs stay quiet', async () => {
  const calls = [];
  const { fields, page } = boot(async (url, init) => { calls.push([url, init]); return response([notice()]); });
  await refresh(fields);
  const beforeRows = text(fields.taskNotificationsList), beforeStatus = fields.taskNotificationsStatus.textContent;
  assert.equal(page.receiveHint(hint(), 'test-token'), true);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '有新的任务通知，请刷新通知。');
  assert.equal(text(fields.taskNotificationsList), beforeRows);
  assert.equal(fields.taskNotificationsStatus.textContent, beforeStatus);
  assert.equal(calls.length, 1);
  assert.equal(page.receiveHint(hint(), 'test-token'), true);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '有新的任务通知，请刷新通知。');
  await refresh(fields);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  assert.equal(page.receiveHint(hint(notice().notification_id), 'test-token'), true);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  assert.equal(calls.every(([, init]) => init.method === 'GET'), true);
});

test('failed refresh keeps hint and a hint arriving during refresh remains visible', async () => {
  const pending = deferred(); let reads = 0;
  const { fields, page } = boot(() => {
    reads++;
    if (reads === 1) return Promise.reject(new Error('private test-token Bearer detail'));
    if (reads === 2) return pending.promise;
    return Promise.resolve(response());
  });
  page.receiveHint(hint(), 'test-token');
  await refresh(fields);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '有新的任务通知，请刷新通知。');
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /private|test-token|Bearer/);
  const request = refresh(fields);
  page.receiveHint(hint('9007199254741101'), 'test-token');
  pending.resolve(response()); await request;
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '有新的任务通知，请刷新通知。');
  await refresh(fields);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
});

test('busy recovery has its own fixed hint while existing loading and buttons stay unchanged', async () => {
  const pending = deferred(); let reads = 0;
  const { fields, page } = boot(() => { reads++; return reads === 1 ? pending.promise : Promise.resolve(response()); });
  const request = refresh(fields);
  page.recover('test-token');
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '连接已恢复，将在当前操作结束后查询通知。');
  assert.match(fields.taskNotificationsStatus.textContent, /正在读取/);
  assert.equal(fields.btnRefreshTaskNotifications.disabled, true);
  assert.equal(reads, 1, 'recovery must not start a competing request');
  pending.resolve(response()); await request;
});

test('token input invalidates connection before scope sync; team input does not', async () => {
  const { fields, input, context, page } = boot(async () => response([notice()]));
  await refresh(fields);
  page.receiveHint(hint(), 'test-token');
  let invalidations = 0;
  context.invalidateTaskNotificationConnection = () => { invalidations++; };
  input('teamId', '201');
  assert.equal(invalidations, 0);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  const epochBeforeToken = page.epoch;
  context.invalidateTaskNotificationConnection = () => {
    invalidations++;
    assert.equal(page.epoch, epochBeforeToken, 'socket invalidation must precede scope sync');
  };
  input('token', 'another-token');
  assert.equal(invalidations, 1);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  assertCleared(fields);
  assert.equal(page.receiveHint(hint('9007199254741111', '201'), 'test-token'), false);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
});
