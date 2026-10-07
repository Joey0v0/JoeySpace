const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'direct-unread.js'), 'utf8');
const peer = '9007199254740993';
const first = '9007199254740995';
const second = '9007199254740994';
const response = (data, status = 200) => ({ ok: status === 200, status, json: async () => ({ code: 0, data }) });
const page = (messages, next = '0') => response({ messages, next_before_message_id: next });
const incoming = (id = first, content = '<script>unsafe</script>') => ({ id, msg_id: 'msg-' + id, from_id: peer, to_id: '42', content_type: 1, content, created_at_unix_ms: 1791355200000 });
const outgoing = (id = second) => ({ id, msg_id: 'msg-' + id, from_id: '42', to_id: peer, content_type: 1, content: 'sent', created_at_unix_ms: 1791355200000 });
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

function setup(fetch = async () => page([])) {
  const fields = {}, calls = [];
  for (const id of ['token', 'toUserId', 'chatType', 'directUnreadCount', 'directUnreadStatus',
    'btnDirectLatest', 'btnDirectRestart', 'btnDirectOlder', 'btnDirectUnreadRefresh', 'btnDirectReadLoaded']) {
    const listeners = {};
    fields[id] = { value: '', textContent: '', disabled: false, onclick: null,
      addEventListener(event, callback) { (listeners[event] ||= []).push(callback); },
      emit(event) { for (const callback of listeners[event] || []) callback(); } };
  }
  fields.token.value = 'first-token'; fields.toUserId.value = peer; fields.chatType.value = '1';
  fields.directHistoryList = { children: [], replaceChildren(...nodes) { this.children = nodes; }, appendChild(node) { this.children.push(node); }, prepend(...nodes) { this.children.unshift(...nodes); } };
  const document = { getElementById: id => fields[id], createElement: () => ({ textContent: '', dataset: {} }) };
  const context = { document, fetch: (...args) => { calls.push(args); return fetch(...args); } };
  vm.runInNewContext(source, context);
  return { page: context.directUnreadPage, fields, calls };
}

test('mount and input changes do not query or acknowledge messages', async () => {
  const s = setup(() => assert.fail('unexpected request'));
  assert.equal(s.calls.length, 0);
  assert.equal(s.fields.btnDirectLatest.disabled, false);
  assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
  for (const id of ['token', 'toUserId', 'chatType']) {
    s.fields[id].emit('input');
    assert.equal(s.calls.length, 0);
    assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
  }
});

test('loaded page marks only incoming IDs and keeps int64 strings', async () => {
  const s = setup(async (url, init) => {
    if (url.endsWith('/messages?limit=20')) return page([incoming(), outgoing()], second);
    if (url.endsWith('/unread')) return response({ peer_id: peer, unread_count: first });
    if (url.endsWith('/read')) return response({ peer_id: peer, unread_count: '0', message_ids: [first] });
    assert.fail('unexpected request: ' + url);
  });
  await s.page.loadLatest();
  assert.equal(s.calls[0][0], '/api/v1/direct/' + peer + '/messages?limit=20');
  assert.equal(s.fields.btnDirectReadLoaded.disabled, false);
  assert.equal(s.fields.directHistoryList.children.length, 2);
  assert.match(s.fields.directHistoryList.children[0].textContent, /<script>unsafe<\/script>/);
  await s.page.refresh();
  assert.equal(s.fields.directUnreadCount.textContent, first);
  await s.page.mark();
  assert.equal(s.calls[2][0], '/api/v1/direct/' + peer + '/read');
  assert.equal(s.calls[2][1].headers.Authorization, 'Bearer first-token');
  assert.deepEqual(JSON.parse(s.calls[2][1].body), { message_ids: [first] });
  assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
  assert.equal(s.fields.directUnreadCount.textContent, '0');
});

test('older page failure retains cursor and confirmation target for retry', async () => {
  let fail = true;
  const s = setup(async url => {
    if (!url.includes('before_message_id')) return page([incoming()], first);
    if (fail) { fail = false; return { ok: false, status: 503 }; }
    return page([incoming(second)], '0');
  });
  await s.page.restart();
  await s.page.loadOlder();
  assert.equal(s.fields.btnDirectReadLoaded.disabled, false);
  await s.page.loadOlder();
  assert.equal(s.calls[1][0], s.calls[2][0]);
  assert.equal(s.fields.directHistoryList.children.length, 2);
  assert.equal(s.fields.btnDirectOlder.disabled, true);
});

test('latest refresh adds new messages without changing older-page cursor', async () => {
  const s = setup(async url => {
    if (url.includes('before_message_id=')) return page([incoming('8')], '0');
    return s.calls.length === 1 ? page([incoming('10')], '10') : page([incoming('11'), incoming('10')], '10');
  });
  await s.page.loadLatest();
  await s.page.loadLatest();
  assert.deepEqual(s.fields.directHistoryList.children.map(row => row.dataset.messageId), ['11', '10']);
  await s.page.loadOlder();
  assert.equal(s.calls[2][0], '/api/v1/direct/' + peer + '/messages?limit=20&before_message_id=10');
  assert.deepEqual(s.fields.directHistoryList.children.map(row => row.dataset.messageId), ['11', '10', '8']);
});

test('invalid page leaves prior cursor and read target unchanged', async () => {
  const s = setup(async url => {
    if (!url.includes('before_message_id=')) return page([incoming('10')], '10');
    if (s.calls.length === 2) return page([incoming('8')], '9');
    return page([incoming('8')], '0');
  });
  await s.page.loadLatest();
  await s.page.loadOlder();
  assert.equal(s.fields.directHistoryList.children.length, 1);
  assert.equal(s.fields.btnDirectReadLoaded.disabled, false);
  await s.page.loadOlder();
  assert.equal(s.calls[1][0], s.calls[2][0]);
  assert.equal(s.fields.directHistoryList.children.length, 2);
});

test('failed confirmation keeps exact IDs for explicit replay', async () => {
  let fail = true;
  const s = setup(async url => {
    if (url.endsWith('/messages?limit=20')) return page([incoming()], '0');
    if (fail) { fail = false; return { ok: false, status: 503 }; }
    return response({ peer_id: peer, unread_count: '0', message_ids: [first] });
  });
  await s.page.loadLatest();
  await s.page.mark();
  assert.equal(s.fields.btnDirectReadLoaded.disabled, false);
  assert.match(s.fields.directUnreadStatus.textContent, /结果不确定/);
  await s.page.mark();
  assert.deepEqual(JSON.parse(s.calls[1][1].body), JSON.parse(s.calls[2][1].body));
  assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
});

test('identity A to B to A drops the old response and clears visible messages', async () => {
  const pending = deferred();
  const s = setup(async () => pending.promise);
  const old = s.page.loadLatest();
  s.fields.token.value = 'second-token'; s.fields.token.emit('input');
  s.fields.token.value = 'first-token'; s.fields.token.emit('input');
  pending.resolve(page([incoming()], '0'));
  await old;
  assert.equal(s.fields.directHistoryList.children.length, 0);
  assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
});

test('denied request clears read targets until scope changes', async () => {
  const s = setup(async url => url.endsWith('/read') ? { ok: false, status: 403 } : page([incoming()], '0'));
  await s.page.loadLatest();
  await s.page.mark();
  assert.equal(s.fields.directHistoryList.children.length, 0);
  assert.equal(s.fields.btnDirectLatest.disabled, true);
  s.fields.toUserId.value = '44'; s.fields.toUserId.emit('input');
  assert.equal(s.fields.btnDirectLatest.disabled, false);
});

test('malformed read echo never becomes successful', async () => {
  const s = setup(async url => url.endsWith('/read')
    ? response({ peer_id: peer, unread_count: '0', message_ids: [second] })
    : page([incoming()], first));
  await s.page.loadLatest();
  await s.page.mark();
  assert.equal(s.fields.btnDirectReadLoaded.disabled, false);
  assert.equal(s.fields.directUnreadCount.textContent, '尚未查询');
  s.page.invalidate();
  assert.equal(s.fields.directHistoryList.children.length, 0);
  assert.equal(s.fields.btnDirectReadLoaded.disabled, true);
});
