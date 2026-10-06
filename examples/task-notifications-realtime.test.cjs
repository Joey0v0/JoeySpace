const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
const plain = value => JSON.parse(JSON.stringify(value));

function element(tag) {
  return { tagName: tag.toUpperCase(), value: '', textContent: '', disabled: false, style: {}, dataset: {}, children: [], listeners: {},
    get innerHTML() { return this._html || this.textContent.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); },
    set innerHTML(value) { this._html = String(value); this.children = []; },
    appendChild(child) { this.children.push(child); return child; },
    replaceChildren(...children) { this.children = children; },
    setAttribute(name, value) { this[name] = value; },
    addEventListener(name, callback) { (this.listeners[name] ||= []).push(callback); } };
}

function boot(handler = async () => notifications()) {
  const fields = {}, inlineInputs = {}, sockets = [], calls = [];
  for (const match of html.matchAll(/<(\w+)[^>]*\bid="([^"]+)"[^>]*>/g)) {
    fields[match[2]] = element(match[1]);
    fields[match[2]].value = match[0].match(/\bvalue="([^"]*)"/)?.[1] || '';
    const input = match[0].match(/\boninput="([^"]*)"/);
    if (input) inlineInputs[match[2]] = input[1];
  }
  fields.token.value = 'test-token'; fields.teamId.value = '200';
  const context = { crypto: require('node:crypto').webcrypto, console, encodeURIComponent,
    location: { protocol: 'http:', hostname: 'gateway.test' },
    document: { getElementById: id => fields[id], createElement: element },
    fetch: (url, init = {}) => {
      calls.push({ url, init });
      if (url === '/api/v1/message/offline') return Promise.resolve(reply([]));
      if (url === '/api/v1/message/offline/ack') return Promise.resolve(reply({}));
      return handler(url, init);
    },
    setInterval: () => assert.fail('notification page must not poll'),
    setTimeout: () => assert.fail('notification page must not schedule automatic retries'),
    localStorage: { setItem: () => assert.fail('notification page must not persist Token') } };
  context.WebSocket = class {
    static OPEN = 1;
    constructor(url) { this.url = url; this.readyState = 0; this.sent = []; this.closeCalls = 0; sockets.push(this); }
    send(payload) { this.sent.push(payload); }
    close() { this.closeCalls++; this.readyState = 3; }
    open() { this.readyState = 1; return this.onopen?.(); }
    message(value) { return this.onmessage?.({ data: JSON.stringify(value) }); }
    closed(reason = 'test close') { this.readyState = 3; return this.onclose?.({ code: 1000, reason }); }
  };
  vm.createContext(context);
  vm.runInContext(html.match(/<script>([\s\S]*?)<\/script>/)[1], context, { filename: 'chat-inline.js' });
  const files = [...html.matchAll(/<script src="\/demo\/([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(files, ['multi-draft-core.js', 'multi-draft-actions.js', 'multi-draft-view.js', 'task-notifications.js', 'task-notifications-view.js', 'team-group-unread.js']);
  for (const file of files) vm.runInContext(fs.readFileSync(path.join(__dirname, file), 'utf8'), context, { filename: file });
  function input(id, value) {
    fields[id].value = value;
    if (inlineInputs[id]) vm.runInContext(inlineInputs[id], context);
    for (const listener of fields[id].listeners.input || []) listener();
  }
  function connect() { context.doConnect(); return sockets.at(-1); }
  return { fields, context, sockets, calls, input, connect, page: context.taskNotificationsPage };
}

function reply(data, status = 200) {
  return { ok: status === 200, status, json: async () => ({ code: status === 200 ? 0 : 1, msg: 'test result', data }) };
}
function notice(id = '9007199254741033', changes = {}) {
  return { notification_id: id, task_id: '9007199254740993', actor_id: '9007199254740995', from_status: 0, to_status: 1,
    created_at_unix_ms: Date.UTC(2026, 9, 5), read_at_unix_ms: 0, ...changes };
}
function notifications(items = [], cursor = '0', status = 200) { return reply({ notifications: items, next_before_notification_id: cursor }, status); }
function hint(id = '9223372036854775807', teamID = '200') {
  return { type: 'task_notification_changed', data: { version: 1, notification_id: id, team_id: teamID } };
}
function pending() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
async function flush() { for (let index = 0; index < 32; index++) await Promise.resolve(); }
function notificationCalls(calls) { return calls.filter(call => call.url.includes('/task-notifications')); }
function refresh(fields) { return fields.btnRefreshTaskNotifications.onclick(); }
function descendants(node) { return [node, ...node.children.flatMap(descendants)]; }
function text(node) { return descendants(node).map(item => item.textContent + ' ' + (item._html || '')).join('\n'); }
function readButton(fields) { return descendants(fields.taskNotificationsList).find(node => node.tagName === 'BUTTON'); }

test('full page starts without sockets or reads; each current connection open recovers latest notices', async () => {
  const { connect, sockets, calls, page, fields, context } = boot(async () => notifications([notice()]));
  assert.equal(sockets.length, 0); assert.equal(calls.length, 0);
  const first = connect(); await first.open(); await flush();
  assert.equal(notificationCalls(calls).length, 1);
  assert.equal(notificationCalls(calls)[0].url, '/api/v1/teams/200/task-notifications?limit=20&before_notification_id=0');
  assert.equal(notificationCalls(calls)[0].init.headers.Authorization, 'Bearer test-token');
  assert.equal(notificationCalls(calls)[0].init.method, 'GET');
  assert.equal(page.state.items[0].read_at_unix_ms, 0);
  assert.equal(fields.wsStatus.textContent, 'Connected');
  context.doDisconnect(); const second = connect(); await second.open(); await flush();
  assert.equal(notificationCalls(calls).length, 2);
  assert.equal(notificationCalls(calls).every(call => call.init.method === 'GET'), true);
  assert.equal(calls.filter(call => call.url === '/api/v1/message/offline').length, 2);
});

test('notification recovery starts independently of pending offline chat pull', async () => {
  const offline = pending();
  const app = boot(async () => notifications([notice()]));
  app.context.fetch = (url, init = {}) => {
    app.calls.push({ url, init });
    return url === '/api/v1/message/offline' ? offline.promise : Promise.resolve(notifications([notice()]));
  };
  const opening = app.connect().open(); await flush();
  assert.equal(notificationCalls(app.calls).length, 1);
  assert.equal(app.page.state.loaded, true);
  offline.resolve(reply([])); await opening;
});

test('hint only lights fixed refresh prompt and duplicate/loaded IDs do not alter records or mark read', async () => {
  const { connect, calls, page, fields } = boot(async () => notifications([notice()]));
  const socket = connect(); await socket.open(); await flush();
  const original = plain(page.state);
  socket.message(hint());
  assert.equal(page.realtime.hasUpdate, true);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '有新的任务通知，请刷新通知。');
  for (let index = 0; index < 3; index++) socket.message(hint());
  assert.deepEqual(plain(page.state), original);
  assert.equal(notificationCalls(calls).length, 1);
  assert.equal(notificationCalls(calls).some(call => call.init.method === 'PUT'), false);
  assert.doesNotMatch(text(fields.logPanel), /task_notification_changed|9223372036854775807/);
  await refresh(fields);
  assert.equal(page.realtime.hasUpdate, false);
  socket.message(hint(notice().notification_id));
  assert.equal(page.realtime.hasUpdate, false, 'already displayed notification is not a new hint');
});

test('invalid reminder envelopes, scope or IDs never fetch or expose event details', async () => {
  const { connect, calls, page, fields } = boot(); const socket = connect(); await socket.open(); await flush();
  const mutations = [
    value => { value.extra = 'private-token payload'; }, value => { value.data.content = '<img src=x>'; },
    value => { value.data = null; }, value => { value.data.version = 2; }, value => { value.data.version = '1'; },
    value => { value.data.team_id = '201'; }, value => { value.data.team_id = 200; },
    value => { value.data.notification_id = 9007199254740993; }, value => { value.data.notification_id = '01'; },
    value => { value.data.notification_id = '0'; }, value => { value.data.notification_id = '9223372036854775808'; },
  ];
  for (const mutate of mutations) { const invalid = hint(); mutate(invalid); socket.message(invalid); }
  assert.equal(page.realtime.hasUpdate, false);
  assert.equal(notificationCalls(calls).length, 1);
  assert.equal(fields.taskNotificationsRealtimeStatus.textContent, '');
  assert.doesNotMatch(text(fields.logPanel), /private-token|task_notification_changed|<img/);
  socket.message(hint()); assert.equal(page.realtime.hasUpdate, true, 'max int64 string is valid');
});

test('a hint arriving during refresh survives that successful response, then later manual refresh clears it', async () => {
  const waiting = pending(); let reads = 0;
  const { connect, page, fields } = boot(() => ++reads === 2 ? waiting.promise : Promise.resolve(notifications()));
  const socket = connect(); await socket.open(); await flush();
  socket.message(hint('100')); const request = refresh(fields);
  socket.message(hint('101')); waiting.resolve(notifications()); await request;
  assert.equal(page.realtime.hasUpdate, true);
  assert.match(fields.taskNotificationsRealtimeStatus.textContent, /有新的任务通知/);
  await refresh(fields); assert.equal(page.realtime.hasUpdate, false);
});

test('reconnect while refresh is busy queues one recovery instead of concurrent or repeated reads', async () => {
  const waiting = pending(); let reads = 0;
  const { connect, context, fields, calls, page } = boot(() => ++reads === 2 ? waiting.promise : Promise.resolve(notifications()));
  const first = connect(); await first.open(); await flush();
  const request = refresh(fields); context.doDisconnect(); const second = connect();
  await second.open(); await second.open(); await flush();
  assert.equal(notificationCalls(calls).length, 2);
  assert.equal(page.realtime.recoveryPending, true);
  assert.match(fields.taskNotificationsRealtimeStatus.textContent, /当前操作结束后查询通知/);
  waiting.resolve(notifications()); await request; await flush();
  assert.equal(notificationCalls(calls).length, 3);
  assert.equal(page.realtime.recoveryPending, false);
  assert.equal(page.state.loading, false);
});

test('recovery waits for explicit read confirmation and never acknowledges another notification', async () => {
  const confirmation = pending(); const calls = [];
  const app = boot((url, init) => {
    calls.push({ url, init });
    return init.method === 'PUT' ? confirmation.promise : Promise.resolve(notifications([notice()]));
  });
  await app.connect().open(); await flush();
  const request = readButton(app.fields).onclick();
  app.context.doDisconnect(); await app.connect().open(); await flush();
  assert.equal(calls.length, 2); assert.equal(app.page.realtime.recoveryPending, true);
  confirmation.resolve(reply({ notification_id: notice().notification_id, read_at_unix_ms: Date.UTC(2026, 9, 5, 0, 1) }));
  await request; await flush();
  assert.equal(calls.length, 3);
  assert.equal(calls.filter(call => call.init.method === 'PUT').length, 1);
  assert.equal(calls[2].init.method, 'GET');
});

for (const status of [401, 403]) test('authorization refusal ' + status + ' clears records and blocks delayed hints until successful GET', async () => {
  let reads = 0;
  const { connect, page, fields } = boot(async () => notifications(reads++ === 0 ? [notice()] : [], '0', reads === 2 ? status : 200));
  const socket = connect(); await socket.open(); await flush(); socket.message(hint('100'));
  await refresh(fields);
  assert.equal(page.state.items.length, 0); assert.equal(page.state.loaded, false);
  assert.equal(page.realtime.hasUpdate, false); assert.equal(page.realtime.recoveryPending, false);
  socket.message(hint('101')); assert.equal(page.realtime.hasUpdate, false);
  await refresh(fields); socket.message(hint('102')); assert.equal(page.realtime.hasUpdate, true);
});

test('team input clears stale scope without reconnecting or fetching, and same-account socket only hints current team', async () => {
  const { connect, input, calls, sockets, page, fields } = boot(); const socket = connect(); await socket.open(); await flush();
  socket.message(hint('100')); input('teamId', '201');
  assert.equal(page.realtime.hasUpdate, false); assert.equal(page.state.loaded, false);
  assert.equal(sockets.length, 1); assert.equal(notificationCalls(calls).length, 1);
  socket.message(hint('101', '200')); assert.equal(page.realtime.hasUpdate, false);
  socket.message(hint('102', '201')); assert.equal(page.realtime.hasUpdate, true);
  assert.match(fields.taskNotificationsRealtimeStatus.textContent, /有新的任务通知/);
});

test('token input A to B to A makes old socket open/message/close inert even after returning to A', async () => {
  const { connect, input, calls, page, fields } = boot(); const old = connect(); await old.open(); await flush();
  input('token', 'B'); input('token', 'test-token');
  const current = connect(); await current.open(); await flush();
  const before = calls.length, status = fields.wsStatus.textContent;
  await old.open(); old.message(hint('100')); old.closed(); await flush();
  assert.equal(calls.length, before); assert.equal(page.realtime.hasUpdate, false);
  assert.equal(fields.wsStatus.textContent, status);
  assert.equal(notificationCalls(calls).length, 2);
});

test('replaced socket cannot reopen, hint or close the current socket, which still sends ordinary chat', async () => {
  const { connect, calls, page, fields, context } = boot(); const old = connect(); await old.open(); await flush();
  const current = connect(); await current.open(); await flush(); const before = calls.length;
  await old.open(); old.message(hint('100')); old.closed(); old.onerror?.({}); await flush();
  assert.equal(calls.length, before); assert.equal(page.realtime.hasUpdate, false);
  assert.equal(fields.wsStatus.textContent, 'Connected');
  fields.toUserId.value = '7'; fields.msgContent.value = 'ordinary chat'; fields.chatType.value = '1'; context.doSend();
  assert.equal(current.sent.length, 1); assert.equal(JSON.parse(current.sent[0]).data.to_id, '7');
});

test('manual disconnect invalidates late socket callbacks without automatic reconnection', async () => {
  const { connect, context, calls, sockets, page, fields } = boot(); const socket = connect(); await socket.open(); await flush();
  context.doDisconnect(); const before = calls.length;
  await socket.open(); socket.message(hint()); socket.closed(); await flush();
  assert.equal(calls.length, before); assert.equal(sockets.length, 1);
  assert.equal(page.realtime.hasUpdate, false); assert.equal(fields.wsStatus.textContent, 'Disconnected');
});

test('same-token successful login resets notices and invalidates the existing socket identity', async () => {
  const { connect, calls, page, fields, context } = boot(async url => {
    if (url === '/api/v1/user/login') return reply({ token: 'test-token' });
    if (url === '/api/v1/user/info') return reply({ id: '7' });
    return notifications([notice()]);
  });
  const old = connect(); await old.open(); await flush(); old.message(hint('100'));
  fields.username.value = 'same-user'; fields.password.value = 'test-password'; await context.doLogin();
  assert.equal(page.state.loaded, false); assert.equal(page.realtime.hasUpdate, false);
  const before = calls.length; await old.open(); old.message(hint('101')); old.closed(); await flush();
  assert.equal(calls.length, before); assert.equal(page.realtime.hasUpdate, false);
  await connect().open(); await flush(); assert.equal(page.state.loaded, true);
});

test('invalid team at connect skips only notification query, preserving ordinary offline pull', async () => {
  const { connect, input, calls, fields } = boot(); input('teamId', ''); await connect().open(); await flush();
  assert.equal(notificationCalls(calls).length, 0);
  assert.equal(calls.filter(call => call.url === '/api/v1/message/offline').length, 1);
  assert.equal(fields.wsStatus.textContent, 'Connected');
});

test('large team scope remains an exact string in reconnect query and only same-team hints are accepted', async () => {
  const { connect, input, calls, page } = boot();
  const teamID = '9223372036854775807'; input('teamId', teamID);
  const socket = connect(); await socket.open(); await flush();
  assert.equal(notificationCalls(calls)[0].url, '/api/v1/teams/' + teamID + '/task-notifications?limit=20&before_notification_id=0');
  socket.message(hint('9007199254740993', '200')); assert.equal(page.realtime.hasUpdate, false);
  socket.message(hint('9007199254740993', teamID)); assert.equal(page.realtime.hasUpdate, true);
  assert.equal(notificationCalls(calls).length, 1);
});

test('failed reconnect lookup does not retry automatically or expose raw HTTP dependency error', async () => {
  const { connect, calls, page, fields } = boot(async () => { throw new Error('private test-token database outage'); });
  await connect().open(); await flush();
  assert.equal(notificationCalls(calls).length, 1); assert.equal(page.state.loading, false);
  assert.equal(page.state.loaded, false); assert.notEqual(page.state.error, '');
  assert.doesNotMatch(fields.taskNotificationsStatus.textContent, /private|test-token|database/);
  await flush(); assert.equal(notificationCalls(calls).length, 1);
});

test('live/offline chat duplicates still render once and acknowledge exact Gateway offline message IDs', async () => {
  const app = boot(async () => notifications());
  const offline = pending();
  const chat = { id: '9007199254740997', msg_id: 'ordinary-message', from_id: '7', to_id: '8', chat_type: 1, content_type: 1,
    sender_type: 1, initiator_id: '0', content: 'visible ordinary chat' };
  app.context.fetch = (url, init = {}) => {
    app.calls.push({ url, init });
    if (url === '/api/v1/message/offline') return offline.promise;
    if (url === '/api/v1/message/offline/ack') return Promise.resolve(reply({}));
    return Promise.resolve(notifications());
  };
  const socket = app.connect(), opening = socket.open(); await flush();
  socket.message({ type: 'chat', data: chat });
  offline.resolve(reply([chat])); await opening; await flush();
  const ack = app.calls.filter(call => call.url === '/api/v1/message/offline/ack');
  assert.equal(ack.length, 1); assert.equal(ack[0].init.method, 'POST');
  assert.equal(ack[0].init.headers.Authorization, 'Bearer test-token');
  assert.deepEqual(JSON.parse(ack[0].init.body), { message_ids: ['9007199254740997'] });
  const rendered = app.fields.logPanel.children.filter(node => text(node).includes('visible ordinary chat') &&
    /New message from|Offline message:/.test(text(node)));
  assert.equal(rendered.length, 1, 'raw RECV log is distinct from the single readable message');
  assert.equal(notificationCalls(app.calls).every(call => call.init.method === 'GET'), true);
});
