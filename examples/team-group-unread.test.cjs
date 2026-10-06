const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'team-group-unread.js'), 'utf8');
const originalScope = { token: 'private-test-token', teamID: '9007199254740993', groupID: '9223372036854775807' };
const ids = ['9007199254740995', '9007199254740994'];
const sortedIDs = [...ids].reverse();
const body = (count = '9007199254740997', marked = null, scope = originalScope) => ({
  code: 0, msg: 'success', data: {
    team_id: scope.teamID, group_id: scope.groupID, unread_count: count,
    ...(marked ? { message_ids: marked } : {})
  }
});
const response = (value = body(), status = 200) => ({ status, ok: status === 200, json: async () => value });
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const clone = value => JSON.parse(JSON.stringify(value));

function setup(fetch = async () => response()) {
  const fields = {}, calls = [];
  for (const id of ['token', 'teamId', 'toUserId', 'chatType', 'teamGroupSelect', 'groupUnreadCount', 'groupUnreadStatus', 'btnGroupUnreadRefresh', 'btnGroupReadLoaded']) {
    const listeners = new Map();
    fields[id] = {
      value: '', disabled: false, textContent: '', onclick: null,
      addEventListener: (event, callback) => {
        if (!listeners.has(event)) listeners.set(event, []);
        listeners.get(event).push(callback);
      },
      emit: event => { for (const callback of listeners.get(event) || []) callback(); }
    };
    Object.defineProperty(fields[id], 'innerHTML', { set() { throw new Error('unsafe HTML write'); } });
  }
  fields.token.value = originalScope.token;
  fields.teamId.value = originalScope.teamID;
  fields.toUserId.value = originalScope.groupID;
  fields.chatType.value = '2';
  const context = {
    document: { getElementById: id => fields[id] },
    fetch: (url, init) => { calls.push({ url, init: clone(init) }); return fetch(url, init); }
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  return {
    page: context.teamGroupUnreadPage, fields, calls,
    load: (loadedIDs = ids, scope = originalScope) => context.teamGroupUnreadPage.loaded(scope, loadedIDs),
    refresh: () => fields.btnGroupUnreadRefresh.onclick(),
    mark: () => fields.btnGroupReadLoaded.onclick()
  };
}

test('mount, history load and input events never automatically query, mark or ACK', async () => {
  const s = setup();
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
  assert.equal(s.fields.btnGroupUnreadRefresh.disabled, false);
  s.load();
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  for (const id of ['token', 'teamId', 'toUserId', 'chatType', 'teamGroupSelect']) {
    s.fields[id].emit('change');
    assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
    s.load();
  }
  s.page.invalidate();
  assert.equal(s.calls.length, 0);
});

test('manual operations retain full int64 strings and POST only this loaded page', async () => {
  const s = setup(async (_url, init) => response(body('9223372036854775807', init.method === 'POST' ? sortedIDs : null)));
  s.load([...ids, ids[0]]);
  await s.refresh();
  assert.equal(s.fields.groupUnreadCount.textContent, '9223372036854775807');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  assert.equal(s.calls[0].url, '/api/v1/teams/9007199254740993/groups/9223372036854775807/unread');
  assert.deepEqual(s.calls[0].init, { method: 'GET', headers: { Authorization: 'Bearer private-test-token' } });
  await s.mark();
  assert.equal(s.calls[1].url, '/api/v1/teams/9007199254740993/groups/9223372036854775807/read');
  assert.deepEqual(JSON.parse(s.calls[1].init.body), { message_ids: sortedIDs });
  assert.deepEqual(s.calls[1].init.headers, { Authorization: 'Bearer private-test-token', 'Content-Type': 'application/json' });
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
  await s.mark();
  assert.equal(s.calls.length, 2);
});

test('empty history replaces the previous target and does not send an empty confirmation', async () => {
  const s = setup();
  s.load();
  assert.equal(s.load([]), true);
  await s.mark();
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
  assert.equal(s.calls.length, 0);
});

test('invalid scopes or loaded IDs never permit POST', async () => {
  for (const id of ['0', '01', '-1', '1e3', '9223372036854775808', 42]) {
    const s = setup();
    assert.equal(s.load([id]), false);
    await s.mark();
    assert.equal(s.calls.length, 0);
  }
  const s = setup();
  assert.equal(s.load(Array(21).fill('1')), false);
  assert.equal(s.load(ids, { ...originalScope, token: 'other' }), false);
  for (const [field, value] of [['token', ''], ['teamId', '9223372036854775808'], ['toUserId', '0'], ['chatType', '1']]) {
    const x = setup();
    x.fields[field].value = value;
    x.fields[field].emit('input');
    await x.refresh();
    assert.equal(x.fields.btnGroupUnreadRefresh.disabled, true);
    assert.equal(x.calls.length, 0);
  }
});

test('busy state suppresses double clicks and alternate actions', async () => {
  const pending = deferred(), s = setup(() => pending.promise);
  s.load();
  const first = s.mark();
  assert.equal(s.fields.btnGroupUnreadRefresh.disabled, true);
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
  await s.mark();
  await s.refresh();
  assert.equal(s.calls.length, 1);
  pending.resolve(response(body('0', sortedIDs)));
  await first;
  assert.equal(s.fields.groupUnreadCount.textContent, '0');
  assert.equal(s.fields.btnGroupUnreadRefresh.disabled, false);
});

test('all scope events invalidate even A to B to A and identical values', async () => {
  for (const id of ['token', 'teamId', 'toUserId', 'chatType', 'teamGroupSelect']) {
    for (const event of id === 'teamGroupSelect' ? ['change'] : ['input', 'change']) {
      const pending = deferred(), s = setup(() => pending.promise);
      s.load();
      const first = s.refresh();
      const old = s.fields[id].value;
      s.fields[id].value = 'other';
      s.fields[id].emit(event);
      s.fields[id].value = old;
      s.fields[id].emit(event);
      pending.resolve(response());
      await first;
      assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
      assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
      assert.equal(s.fields.btnGroupUnreadRefresh.disabled, false);
    }
  }
});

test('same-token login invalidate drops pending result and target', async () => {
  const pending = deferred(), s = setup(() => pending.promise);
  s.load();
  const first = s.mark();
  s.page.invalidate();
  pending.resolve(response(body('1', sortedIDs)));
  await first;
  assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
});

test('scope change without an event is detected before a response is applied', async () => {
  const pending = deferred(), s = setup(() => pending.promise);
  const first = s.refresh();
  s.fields.toUserId.value = '300';
  pending.resolve(response());
  await first;
  assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
});

test('JSON decoding races cannot bypass invalidation or clear a later history page', async () => {
  const decoding = deferred(), entered = deferred();
  const s = setup(async () => ({ status: 200, ok: true, json: () => { entered.resolve(); return decoding.promise; } }));
  s.load();
  const first = s.mark();
  await entered.promise;
  s.load(['17']);
  decoding.resolve(body('0', sortedIDs));
  await first;
  assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
});

test('new page supersedes old mark while new GET may independently complete', async () => {
  const old = deferred();
  let attempt = 0;
  const s = setup(() => ++attempt === 1 ? old.promise : Promise.resolve(response(body('7'))));
  s.load();
  const first = s.mark();
  s.load(['17']);
  await s.refresh();
  old.resolve(response(body('0', sortedIDs)));
  await first;
  assert.equal(s.fields.groupUnreadCount.textContent, '7');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
});

test('401 or 403 clears and blocks until input or explicit login invalidation', async () => {
  for (const status of [401, 403]) {
    let attempt = 0;
    const s = setup(async () => ++attempt === 1 ? response({ msg: 'private detail' }, status) : response());
    s.load();
    await s.refresh();
    assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
    assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
    assert.equal(s.fields.btnGroupUnreadRefresh.disabled, true);
    assert.equal(s.load(), false);
    await s.refresh();
    await s.mark();
    assert.equal(s.calls.length, 1);
    s.fields.token.emit('input');
    await s.refresh();
    assert.equal(s.calls.length, 2);
    assert.equal(s.fields.groupUnreadCount.textContent, '9007199254740997');
  }
});

test('late authorization denial after input cannot block a fresh generation', async () => {
  const old = deferred(), s = setup(() => old.promise);
  const first = s.refresh();
  s.fields.token.emit('input');
  s.load(['17']);
  old.resolve(response({}, 403));
  await first;
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  assert.equal(s.fields.btnGroupUnreadRefresh.disabled, false);
});

test('read errors make count unknown while preserving explicit page retry', async () => {
  let attempt = 0;
  const s = setup(async () => {
    if (++attempt === 1) return response();
    if (attempt === 2) throw new Error('private-test-token SQL password');
    return response(body('0', sortedIDs));
  });
  s.load();
  await s.refresh();
  await s.refresh();
  assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  assert.equal(s.fields.groupUnreadStatus.textContent, '未读数读取失败，请显式刷新重试。');
  await s.mark();
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
});

test('uncertain confirmations retain exact targets and require explicit retry', async () => {
  for (const failure of [async () => { throw new Error('private-test-token'); }, async () => response({}, 503), async () => ({ status: 200, ok: true, json: async () => { throw new Error('invalid JSON private detail'); } })]) {
    let attempt = 0;
    const s = setup(async () => ++attempt === 1 ? failure() : response(body('0', sortedIDs)));
    s.load();
    await s.mark();
    assert.equal(s.calls.length, 1);
    assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
    assert.equal(s.fields.groupUnreadStatus.textContent, '确认结果尚未确认，请刷新未读数或显式重试本页确认。');
    await s.mark();
    assert.equal(s.calls[1].init.body, s.calls[0].init.body);
    assert.equal(s.fields.groupUnreadCount.textContent, '0');
  }
});

test('unread response accepts only canonical nonnegative int64 strings and exact scope', async () => {
  for (const count of [-1, 0, 9007199254740992, '-1', '00', '01', '1e3', ' 1', '9223372036854775808', null]) {
    const s = setup(async () => response(body(count)));
    s.load();
    await s.refresh();
    assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
    assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  }
  for (const change of [data => { data.team_id = '200'; }, data => { data.group_id = 300; }, data => { data.content = '<script>private</script>'; }]) {
    const value = body();
    change(value.data);
    const s = setup(async () => response(value));
    await s.refresh();
    assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
    assert.equal(s.fields.groupUnreadStatus.textContent.includes('private'), false);
  }
});

test('mark requires the full exact echo set; bad echoes retain the target', async () => {
  for (const echoed of [['9007199254740994'], ['9007199254740994', '9007199254740994'], [...sortedIDs, '17'], ['9007199254740994', '17'], [9007199254740994, '9007199254740995'], ['01', '9007199254740995']]) {
    const s = setup(async () => response(body('0', echoed)));
    s.load();
    await s.mark();
    assert.equal(s.fields.groupUnreadCount.textContent, '尚未查询');
    assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  }
  const s = setup(async () => response(body('0', ids)));
  s.load();
  await s.mark();
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
});
