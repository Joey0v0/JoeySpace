const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const clone = value => JSON.parse(JSON.stringify(value));
const firstID = 9223372036854775807n;
const rows = (count = 20, top = firstID) => Array.from({ length: count }, (_, index) => ({
  notification_id: String(top - BigInt(index)), task_id: '9007199254740993', actor_id: '9007199254740995',
  from_status: 0, to_status: 1, created_at_unix_ms: 1791000000123, read_at_unix_ms: 0
}));
const body = (items = rows(), cursor = items.length === 20 ? items[19].notification_id : '0') =>
  ({ code: 0, msg: 'success', data: { notifications: items, next_before_notification_id: cursor } });
const response = (value = body(), status = 200) => ({ ok: status === 200, status, json: async () => value });
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
function setup(fetch) {
  const context = {};
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'task-notifications.js'), 'utf8'), context);
  const scope = { token: 'private-test-token', teamID: '9007199254740997' };
  const calls = [], changes = [];
  const controller = new context.TaskNotificationsController({
    fetch: (url, init) => { calls.push({ url, init }); return fetch(url, init); },
    getScope: () => scope, onChange: state => changes.push(clone(state))
  });
  return { controller, scope, calls, changes };
}

test('manual refresh and older-page loading retain exact large IDs and finish pagination', async () => {
  let page = 0;
  const { controller: c, calls, changes } = setup(async () => response(++page === 1 ? body() : body(rows(2, firstID - 20n))));
  assert.equal(calls.length, 0);
  await c.loadMore();
  assert.equal(calls.length, 0);
  await c.refresh();
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740997/task-notifications?limit=20&before_notification_id=0');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer private-test-token');
  assert.equal(c.state.cursor, String(firstID - 19n));
  await c.loadMore();
  assert.equal(calls[1].url.endsWith('before_notification_id=' + String(firstID - 19n)), true);
  assert.equal(c.state.items.length, 22);
  assert.equal(c.state.items[21].notification_id, String(firstID - 21n));
  assert.equal(c.state.items[0].task_id, '9007199254740993');
  assert.equal(c.state.cursor, '0');
  await c.loadMore();
  assert.equal(calls.length, 2);
  assert.equal(changes.some(state => state.loading), true);
  assert.equal(JSON.stringify(c.state).includes('private-test-token'), false);
});

test('empty result is a successfully loaded final page, and refresh replaces old records', async () => {
  let page = 0;
  const { controller: c, calls } = setup(async () => response(++page === 1 ? body(rows(2)) : body([])));
  await c.refresh();
  assert.equal(c.state.items.length, 2);
  await c.refresh();
  assert.deepEqual(clone(c.state), { items: [], cursor: '0', loaded: true, loading: false, error: '' });
  await c.loadMore();
  assert.equal(calls.length, 2);
});

test('invalid or missing identity/team never sends a request', async () => {
  const { controller: c, scope, calls } = setup(async () => response());
  for (const teamID of ['', '0', '01', '-1', '+1', '1.0', '1e3', ' 1', '1 ', '9223372036854775808', 12, null]) {
    scope.teamID = teamID;
    await c.refresh();
    assert.equal(c.state.loaded, false);
    assert.equal(c.state.items.length, 0);
    assert.notEqual(c.state.error, '');
  }
  scope.teamID = '1';
  for (const token of ['', '   ', null, 42]) {
    scope.token = token;
    await c.refresh();
  }
  assert.equal(calls.length, 0);
});

test('first-page refresh failure removes old records and reports only safe fixed text', async () => {
  let attempt = 0;
  const { controller: c } = setup(async () => {
    if (++attempt === 1) return response();
    throw new Error('private-test-token SQL server password');
  });
  await c.refresh();
  await c.refresh();
  assert.deepEqual(clone(c.state), { items: [], cursor: '0', loaded: false, loading: false, error: '通知读取失败，请重试' });
});

test('older-page network and service failures preserve records and cursor for an explicit retry', async () => {
  let attempt = 0;
  const { controller: c, calls } = setup(async () => {
    if (++attempt === 1) return response();
    if (attempt === 2) throw new Error('private-test-token');
    if (attempt === 3) return response({ msg: 'secret detail' }, 503);
    return response(body(rows(1, firstID - 20n)));
  });
  await c.refresh();
  const saved = clone(c.state.items), cursor = c.state.cursor;
  for (let i = 0; i < 2; i++) {
    await c.loadMore();
    assert.deepEqual(clone(c.state.items), saved);
    assert.equal(c.state.cursor, cursor);
    assert.equal(c.state.loaded, true);
    assert.equal(c.state.error, '通知读取失败，请重试');
  }
  await c.loadMore();
  assert.equal(c.state.items.length, 21);
  assert.equal(c.state.error, '');
  assert.equal(calls.slice(1).every(call => call.url.endsWith('before_notification_id=' + cursor)), true);
});

for (const status of [401, 403]) {
  test('permission failure ' + status + ' clears prior records and requires refresh instead of older-page retry', async () => {
    let attempt = 0, parsedError = false;
    const { controller: c, calls } = setup(async () => {
      if (++attempt === 2) return { ok: false, status, json: async () => { parsedError = true; throw new Error('private-test-token'); } };
      return response(body(attempt === 1 ? rows() : rows(1)));
    });
    await c.refresh();
    await c.loadMore();
    assert.equal(c.state.items.length, 0);
    assert.equal(c.state.cursor, '0');
    assert.equal(c.state.loaded, false);
    assert.equal(c.state.loading, false);
    assert.notEqual(c.state.error, '');
    assert.equal(parsedError, false);
    await c.loadMore();
    assert.equal(calls.length, 2);
    await c.refresh();
    assert.equal(calls[2].url.endsWith('before_notification_id=0'), true);
    assert.equal(c.state.loaded, true);
    assert.equal(c.state.error, '');
  });
}

test('malformed whole pages cannot append partial records or advance the cursor', async () => {
  const mutations = [
    value => { value.code = 1; }, value => { value.data = null; },
    value => { value.data.notifications = null; }, value => { value.data.notifications.push(rows(1, firstID - 100n)[0]); },
    value => { value.data.next_before_notification_id = 0; }, value => { value.data.next_before_notification_id = '01'; },
    value => { value.data.next_before_notification_id = '9223372036854775808'; },
    value => { value.data.next_before_notification_id = value.data.notifications[0].notification_id; },
    value => { value.data.notifications.pop(); }, value => { value.data.notifications[1] = null; },
    value => { value.data.notifications[1].notification_id = value.data.notifications[0].notification_id; },
    value => { value.data.notifications[1].notification_id = '9223372036854775807'; },
    value => { value.data.notifications[1].notification_id = Number(value.data.notifications[1].notification_id); },
    value => { value.data.notifications[1].task_id = '01'; }, value => { value.data.notifications[1].actor_id = '0'; },
    value => { value.data.notifications[1].from_status = 1; }, value => { value.data.notifications[1].from_status = -1; },
    value => { value.data.notifications[1].to_status = 3; }, value => { value.data.notifications[1].to_status = '2'; },
    value => { value.data.notifications[1].created_at_unix_ms = 0; },
    value => { value.data.notifications[1].created_at_unix_ms = 1.2; },
    value => { value.data.notifications[1].created_at_unix_ms = 253402300800000; },
    value => { delete value.data.notifications[1].read_at_unix_ms; },
    ...[null, '0', -1, 1.5, 1791000000122, 253402300800000, Number.MAX_SAFE_INTEGER + 1].map(time =>
      value => { value.data.notifications[1].read_at_unix_ms = time; })
  ];
  for (const mutate of mutations) {
    let attempt = 0;
    const invalid = body(rows(20, firstID - 20n));
    mutate(invalid);
    const { controller: c } = setup(async () => response(++attempt === 1 ? body() : invalid));
    await c.refresh();
    const before = clone(c.state);
    await c.loadMore();
    assert.deepEqual(clone(c.state.items), before.items);
    assert.equal(c.state.cursor, before.cursor);
    assert.equal(c.state.error, '通知读取失败，请重试');
  }
});

test('an ID equal to the requested cursor is rejected, including a one-item final page', async () => {
  let attempt = 0;
  const { controller: c } = setup(async () => response(++attempt === 1 ? body() : body(rows(1, firstID - 19n))));
  await c.refresh();
  await c.loadMore();
  assert.equal(c.state.items.length, 20);
  assert.equal(c.state.cursor, String(firstID - 19n));
  assert.notEqual(c.state.error, '');
});

test('duplicate refresh and load-more clicks share one in-flight operation', async () => {
  const pending = deferred();
  const { controller: c, calls } = setup(() => pending.promise);
  const first = c.refresh();
  await c.refresh();
  await c.loadMore();
  assert.equal(calls.length, 1);
  assert.equal(c.state.loading, true);
  pending.resolve(response());
  await first;
  assert.equal(c.state.items.length, 20);
});

test('duplicate older-page clicks cannot append the same page twice', async () => {
  const pending = deferred();
  let attempt = 0;
  const { controller: c, calls } = setup(() => ++attempt === 1 ? Promise.resolve(response()) : pending.promise);
  await c.refresh();
  const first = c.loadMore();
  await c.loadMore();
  await c.refresh();
  assert.equal(calls.length, 2);
  pending.resolve(response(body(rows(1, firstID - 20n))));
  await first;
  assert.equal(c.state.items.length, 21);
});

test('team change permits new loading immediately and stale fetch success cannot touch its state', async () => {
  const old = deferred(), current = deferred();
  let attempt = 0;
  const { controller: c, scope } = setup(() => (++attempt === 1 ? old : current).promise);
  const first = c.refresh();
  scope.teamID = '2'; c.syncScope();
  assert.equal(c.state.items.length, 0);
  const second = c.refresh();
  old.resolve(response());
  await first;
  assert.equal(c.state.loading, true);
  assert.equal(c.state.loaded, false);
  current.resolve(response(body(rows(1, 100n))));
  await second;
  assert.equal(c.state.items[0].notification_id, '100');
});

test('token A to B to A input events invalidate the original A request even when values match again', async () => {
  const old = deferred(), current = deferred();
  let attempt = 0;
  const { controller: c, scope } = setup(() => (++attempt === 1 ? old : current).promise);
  const first = c.refresh();
  scope.token = 'B'; c.syncScope();
  scope.token = 'private-test-token'; c.syncScope();
  const second = c.refresh();
  old.reject(new Error('private-test-token'));
  await first;
  assert.equal(c.state.loading, true);
  assert.equal(c.state.error, '');
  current.resolve(response(body([])));
  await second;
  assert.equal(c.state.loaded, true);
});

for (const outcome of ['resolve', 'reject']) {
  test('same-identity login reset isolates delayed JSON ' + outcome + ' and old finally from a new request', async () => {
    const json = deferred(), enteredJSON = deferred(), current = deferred();
    let attempt = 0;
    const { controller: c } = setup(() => {
      if (++attempt === 1) return Promise.resolve({ ok: true, status: 200, json: () => { enteredJSON.resolve(); return json.promise; } });
      return current.promise;
    });
    const first = c.refresh();
    await enteredJSON.promise;
    c.reset();
    const second = c.refresh();
    if (outcome === 'resolve') json.resolve(body());
    else json.reject(new Error('private-test-token'));
    await first;
    assert.equal(c.state.loading, true);
    assert.equal(c.state.items.length, 0);
    assert.equal(c.state.error, '');
    current.resolve(response(body([])));
    await second;
    assert.equal(c.state.loaded, true);
  });
}

test('scope changes discovered after awaiting JSON clear records without input-event assistance', async () => {
  const json = deferred(), entered = deferred();
  const { controller: c, scope } = setup(async () => ({ ok: true, status: 200, json: () => { entered.resolve(); return json.promise; } }));
  const request = c.refresh();
  await entered.promise;
  scope.teamID = '8';
  json.resolve(body());
  await request;
  assert.deepEqual(clone(c.state), { items: [], cursor: '0', loaded: false, loading: false, error: '' });
});

test('JSON rejection and synchronous fetch failure are caught without exposing raw errors', async () => {
  for (const fetch of [() => { throw new Error('private-test-token'); },
    async () => ({ ok: true, status: 200, json: async () => { throw new Error('private-test-token'); } })]) {
    const { controller: c } = setup(fetch);
    await c.refresh();
    assert.equal(c.state.loading, false);
    assert.equal(c.state.error, '通知读取失败，请重试');
    assert.equal(JSON.stringify(c.state).includes('private-test-token'), false);
  }
});

const readBody = (id, time = 1791000000999) => ({ code: 0, data: { notification_id: id, read_at_unix_ms: time } });

test('GET retains unread and already-read records without marking any of them', async () => {
  const items = rows(2); items[1].read_at_unix_ms = 1791000000999;
  const { controller: c, calls } = setup(async () => response(body(items)));
  await c.refresh();
  assert.deepEqual(clone(c.state.items), items);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].init.method, 'GET');
  await c.markRead(items[1].notification_id);
  assert.equal(calls.length, 1, 'already-read records are not submitted again');
});

test('explicit mark uses exact large team/notification IDs, no body, and changes only the selected read time', async () => {
  const items = rows(), target = items[7].notification_id;
  const { controller: c, calls } = setup(async (_, init) => response(init.method === 'PUT' ? readBody(target) : body(items)));
  await c.refresh();
  const before = clone(c.state);
  await c.markRead(target);
  assert.equal(calls[1].url, '/api/v1/teams/9007199254740997/task-notifications/' + target + '/read');
  assert.deepEqual(clone(calls[1].init), { method: 'PUT', headers: { Authorization: 'Bearer private-test-token' } });
  const expected = before.items; expected[7].read_at_unix_ms = 1791000000999;
  assert.deepEqual(clone(c.state.items), expected);
  assert.equal(c.state.cursor, before.cursor);
  assert.equal(c.state.loaded, true);
  await c.markRead(target);
  assert.equal(calls.length, 2);
});

test('unknown, malformed, unloaded and noncurrent records cannot be marked', async () => {
  const { controller: c, scope, calls } = setup(async () => response(body(rows(1))));
  await c.markRead(String(firstID));
  await c.refresh();
  for (const id of ['1', '0', '01', '-1', '9223372036854775808', Number(firstID), null]) await c.markRead(id);
  assert.equal(calls.length, 1);
  scope.teamID = '2';
  await c.markRead(String(firstID));
  assert.equal(calls.length, 1);
  assert.equal(c.state.loaded, false);
});

test('confirmation and reads are mutually exclusive, including duplicate mark and another record', async () => {
  const pending = deferred();
  const { controller: c, calls } = setup((_, init) => init.method === 'PUT' ? pending.promise : Promise.resolve(response()));
  await c.refresh();
  const request = c.markRead(String(firstID));
  assert.equal(c.state.loading, true);
  await c.markRead(String(firstID)); await c.markRead(String(firstID - 1n)); await c.loadMore(); await c.refresh();
  assert.equal(calls.length, 2);
  pending.resolve(response(readBody(String(firstID)))); await request;
  assert.equal(c.state.loading, false);
  assert.equal(c.state.items[1].read_at_unix_ms, 0);
  const reading = deferred();
  const { controller: other, calls: otherCalls } = setup(() => reading.promise);
  const load = other.refresh(); await other.markRead(String(firstID));
  assert.equal(otherCalls.length, 1);
  reading.resolve(response()); await load;
});

test('lost mark response allows same-ID retry and retains the server first time after refresh', async () => {
  let puts = 0, saved = 0;
  const { controller: c, calls } = setup(async (_, init) => {
    if (init.method === 'PUT') {
      saved ||= 1791000000999;
      if (++puts === 1) throw new Error('private-test-token response lost after commit');
      return response(readBody(String(firstID), saved));
    }
    const items = rows(2); items[0].read_at_unix_ms = saved;
    return response(body(items));
  });
  await c.refresh(); await c.markRead(String(firstID));
  assert.equal(c.state.items[0].read_at_unix_ms, 0);
  assert.equal(c.state.error, '未能确认，请刷新或重试');
  await c.markRead(String(firstID));
  assert.equal(c.state.items[0].read_at_unix_ms, saved);
  assert.equal(c.state.error, '');
  assert.equal(calls[1].url, calls[2].url);
  await c.refresh();
  assert.equal(c.state.items[0].read_at_unix_ms, 1791000000999);
  await c.markRead(String(firstID));
  assert.equal(puts, 2);
});

test('invalid confirmation and service failures retain unread content and exact pagination for retry', async () => {
  const invalidBodies = [null, {}, { code: 1 }, { code: 0, data: null }, readBody(String(firstID - 1n)),
    readBody(Number(firstID)), ...[undefined, null, '1791000000999', 0, -1, 1.5, 1791000000122, 253402300800000].map(time => {
      const value = readBody(String(firstID)); value.data.read_at_unix_ms = time; return value;
    })];
  const failures = [() => { throw new Error('private-test-token'); },
    async () => ({ ok: true, status: 200, json: async () => { throw new Error('private-test-token'); } }),
    ...[404, 503, 504].map(status => async () => response({ msg: 'private-test-token' }, status)),
    ...invalidBodies.map(value => async () => response(value))];
  for (const failure of failures) {
    const { controller: c } = setup((url, init) => init.method === 'PUT' ? failure() : Promise.resolve(response()));
    await c.refresh(); const saved = clone(c.state);
    await c.markRead(String(firstID));
    assert.deepEqual(clone(c.state.items), saved.items);
    assert.equal(c.state.cursor, saved.cursor);
    assert.equal(c.state.loaded, true);
    assert.equal(c.state.loading, false);
    assert.equal(c.state.error, '未能确认，请刷新或重试');
  }
});

for (const status of [401, 403]) test('mark permission refusal ' + status + ' clears all records without parsing private response', async () => {
  let parsed = false;
  const { controller: c, calls } = setup(async (_, init) => init.method === 'PUT'
    ? { ok: false, status, json: async () => { parsed = true; throw new Error('private-test-token'); } } : response());
  await c.refresh(); await c.markRead(String(firstID));
  assert.equal(c.state.items.length, 0); assert.equal(c.state.cursor, '0'); assert.equal(c.state.loaded, false);
  assert.equal(c.state.loading, false); assert.equal(parsed, false);
  assert.notEqual(c.state.error, ''); assert.equal(c.state.error.includes('private-test-token'), false);
  await c.markRead(String(firstID)); assert.equal(calls.length, 2);
});

for (const outcome of ['resolve', 'reject']) test('mark delayed JSON ' + outcome + ' cannot affect new generation or its loading', async () => {
  const entered = deferred(), json = deferred(), current = deferred(); let gets = 0;
  const { controller: c } = setup(async (_, init) => init.method === 'PUT'
    ? { ok: true, status: 200, json: () => { entered.resolve(); return json.promise; } }
    : ++gets === 1 ? response(body(rows(1))) : current.promise);
  await c.refresh(); const old = c.markRead(String(firstID)); await entered.promise;
  c.reset(); const next = c.refresh();
  if (outcome === 'resolve') json.resolve(readBody(String(firstID))); else json.reject(new Error('private-test-token'));
  await old;
  assert.equal(c.state.loading, true); assert.equal(c.state.items.length, 0); assert.equal(c.state.error, '');
  current.resolve(response(body(rows(1)))); await next;
  assert.equal(c.state.items[0].read_at_unix_ms, 0);
});

test('paging after confirmation keeps marked first-page records and loaded older read timestamps', async () => {
  let gets = 0;
  const older = rows(1, firstID - 20n); older[0].read_at_unix_ms = 1791000000777;
  const { controller: c } = setup(async (_, init) => response(init.method === 'PUT'
    ? readBody(String(firstID)) : ++gets === 1 ? body() : body(older)));
  await c.refresh(); await c.markRead(String(firstID)); await c.loadMore();
  assert.equal(c.state.items.length, 21);
  assert.equal(c.state.items[0].read_at_unix_ms, 1791000000999);
  assert.equal(c.state.items[1].read_at_unix_ms, 0);
  assert.equal(c.state.items[20].read_at_unix_ms, 1791000000777);
  assert.equal(c.state.cursor, '0');
});
