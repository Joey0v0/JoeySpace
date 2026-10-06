const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
const unreadSource = fs.readFileSync(path.join(__dirname, 'team-group-unread.js'), 'utf8');
const scope = { token: 'recovery-token-A', team: '9007199254740993', group: '9007199254740994' };
const oldID = '9007199254741060', lateID = '9007199254741001', newID = '9007199254741050';
const firstIDs = Array.from({ length: 20 }, (_, index) => (9007199254741100n - BigInt(index)).toString());
const clone = value => JSON.parse(JSON.stringify(value));
const sorted = ids => [...new Set(ids)].sort((a, b) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0);
const response = (body, status = 200) => ({ ok: status === 200, status, json: async () => body });
const message = id => ({ id, msg_id: 'message-' + id, from_id: '42', sender_type: 1, content_type: 1, content: 'message ' + id });
const history = (ids, cursor = '0') => ({ code: 0, data: { messages: ids.map(message), next_before_message_id: cursor } });
const unread = (count, ids = null) => ({ code: 0, msg: 'success', data: {
  team_id: scope.team, group_id: scope.group, unread_count: count,
  ...(ids ? { message_ids: ids } : {})
} });

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

// The DOM and HTTP transport are substitutes. History paging, de-duplication,
// inline identity handlers, and unread controls all execute their actual source.
function page(fetch) {
  let context;
  function element(tag = 'div') {
    const listeners = new Map();
    let text = '', markup = '';
    return {
      tagName: tag.toUpperCase(), value: '', disabled: false, children: [], dataset: {}, style: {},
      get textContent() { return text; },
      set textContent(value) { text = String(value); markup = text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
      get innerHTML() { return markup; },
      set innerHTML(value) { markup = String(value); },
      get options() { return this.children; },
      appendChild(child) { this.children.push(child); return child; },
      replaceChildren(...children) { this.children = children; },
      setAttribute(name, value) { this[name] = value; },
      addEventListener(event, callback) {
        if (!listeners.has(event)) listeners.set(event, []);
        listeners.get(event).push(callback);
      },
      emit(event) {
        if (typeof this['on' + event] === 'function') this['on' + event]();
        for (const callback of listeners.get(event) || []) callback();
      }
    };
  }
  const fields = {}, actions = new Map(), calls = [];
  for (const match of html.matchAll(/<(input|select|textarea|button|div|span|small|p)\b([^>]*)>/g)) {
    const [, tag, attributes] = match;
    const id = attributes.match(/\bid="([^"]+)"/);
    const node = element(tag);
    if (id) fields[id[1]] = node;
    node.value = attributes.match(/\bvalue="([^"]*)"/)?.[1] || '';
    node.disabled = /\bdisabled\b/.test(attributes);
    for (const binding of attributes.matchAll(/\bon(input|change|click)="([^"]+)"/g)) {
      node['on' + binding[1]] = () => vm.runInContext(binding[2], context);
      if (binding[1] === 'click') actions.set(binding[2], node);
    }
  }
  fields.token.value = scope.token;
  fields.teamId.value = scope.team;
  fields.toUserId.value = scope.group;
  fields.chatType.value = '2';
  context = vm.createContext({
    document: {
      getElementById: id => fields[id],
      createElement: tag => element(tag)
    },
    location: { protocol: 'http:', hostname: 'gateway.test' },
    fetch: (url, init = {}) => {
      calls.push({ url, init: clone(init) });
      assert.equal(url.includes('/message/offline'), false, 'history/unread recovery must not pull or ACK offline deliveries');
      return fetch(url, init);
    },
    crypto: { getRandomValues: bytes => { bytes.fill(1); return bytes; } },
    encodeURIComponent
  });
  const inline = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
  assert.ok(inline.length, 'the actual page inline script is required');
  for (const block of inline) vm.runInContext(block[1], context, { filename: 'chat.html' });
  vm.runInContext(unreadSource, context, { filename: 'team-group-unread.js' });
  return {
    context, fields, calls,
    older: () => actions.get('doLoadTeamGroupHistory()').onclick(),
    latest: () => actions.get('doRefreshTeamGroupHistory()').onclick(),
    restart: () => {
      const button = actions.get('doRestartTeamGroupHistory()');
      assert.ok(button, 'the actual HTML must bind the explicit restart button');
      return button.onclick();
    },
    refresh: () => fields.btnGroupUnreadRefresh.onclick(),
    mark: () => fields.btnGroupReadLoaded.onclick(),
    change: (id, value) => { fields[id].value = value; fields[id].emit('input'); },
    histories: () => calls.filter(call => call.url.includes('/messages?')),
    marks: () => calls.filter(call => call.url.endsWith('/read')),
    queries: () => calls.filter(call => call.url.endsWith('/unread')),
    rendered: id => fields.logPanel.children.filter(entry => entry.innerHTML.includes('"msg_id":"message-' + id + '"'))
  };
}

function assertMark(call, ids) {
  assert.equal(call.url, '/api/v1/teams/' + scope.team + '/groups/' + scope.group + '/read');
  assert.equal(call.init.method, 'POST');
  assert.deepEqual(call.init.headers, { Authorization: 'Bearer ' + scope.token, 'Content-Type': 'application/json' });
  assert.deepEqual(JSON.parse(call.init.body), { message_ids: sorted(ids) });
}

test('an ended history finds a late low ID only after explicit restart and confirms precisely the reloaded page', async () => {
  const messages = [...firstIDs, oldID], read = new Set();
  const remaining = () => String(messages.filter(id => !read.has(id)).length);
  const s = page(async (url, init) => {
    if (url.endsWith('/read')) {
      const ids = JSON.parse(init.body).message_ids;
      ids.forEach(id => read.add(id));
      return response(unread(remaining(), ids));
    }
    if (url.endsWith('/unread')) return response(unread(remaining()));
    const parsed = new URL(url, 'http://gateway.test');
    assert.equal(parsed.searchParams.get('limit'), '20');
    const before = parsed.searchParams.get('before_message_id');
    const eligible = messages.filter(id => !before || BigInt(id) < BigInt(before));
    const batch = eligible.slice(0, 20);
    return response(history(batch, eligible.length > 20 ? batch.at(-1) : '0'));
  });
  assert.equal(s.calls.length, 0, 'mount must not fetch');
  await s.older();
  assert.equal(s.marks().length, 0, 'history is not read confirmation');
  await s.mark();
  assertMark(s.marks()[0], firstIDs);
  await s.older();
  await s.mark();
  assertMark(s.marks()[1], [oldID]);
  assert.equal(s.fields.groupUnreadCount.textContent, '0');
  await s.older();
  assert.equal(s.histories().length, 2, 'old traversal has ended');

  messages.push(lateID);
  await s.refresh();
  assert.equal(s.fields.groupUnreadCount.textContent, '1');
  assert.equal(s.marks().length, 2, 'querying an unread count never marks or retries');
  assert.equal(s.histories().length, 2, 'querying unread never starts a history traversal');
  await s.older();
  assert.equal(s.histories().length, 2);
  await s.latest();
  assert.equal(s.histories().length, 3);
  await s.older();
  assert.equal(s.histories().length, 3, 'refresh latest leaves the old ended cursor unchanged');

  await s.restart();
  assert.equal(s.histories().length, 4);
  assert.equal(s.histories()[3].url, '/api/v1/teams/' + scope.team + '/groups/' + scope.group + '/messages?limit=20');
  await s.older();
  assert.equal(new URL(s.histories()[4].url, 'http://gateway.test').searchParams.get('before_message_id'), firstIDs.at(-1));
  assert.equal(s.marks().length, 2, 're-traversal is still an explicit read action');
  assert.equal(s.rendered(oldID).length, 1, 'existing messages display once');
  assert.equal(s.rendered(lateID).length, 1, 'late low ID becomes visible');
  for (const id of firstIDs) assert.equal(s.rendered(id).length, 1);
  assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
  await s.mark();
  assertMark(s.marks()[2], [oldID, lateID]);
  assert.equal(s.fields.groupUnreadCount.textContent, '0');
  assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
  assert.equal(s.queries().length, 1);
  assert.equal(s.calls.filter(call => call.init.method === 'POST').length, 3);
});

test('a lost confirmation response retains identical IDs for explicit retry; failed GET never retries automatically', async t => {
  for (const failure of ['transport', 'HTTP 503', 'invalid echo']) {
    await t.test(failure, async () => {
      let markCalls = 0, queryCalls = 0, committed = false;
      const s = page(async (url, init) => {
        if (url.includes('/messages?')) return response(history([oldID, lateID]));
        if (url.endsWith('/read')) {
          const ids = JSON.parse(init.body).message_ids;
          committed = true;
          if (++markCalls === 1) {
            if (failure === 'transport') throw new Error('response lost');
            if (failure === 'HTTP 503') return response(null, 503);
            return response(unread('0', [newID]));
          }
          return response(unread('0', ids));
        }
        assert.ok(url.endsWith('/unread'));
        if (++queryCalls === 1) return response(null, 503);
        return response(unread(committed ? '0' : '2'));
      });
      await s.older();
      await s.mark();
      assertMark(s.marks()[0], [oldID, lateID]);
      assert.equal(markCalls, 1);
      assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
      assert.match(s.fields.groupUnreadStatus.textContent, /尚未确认/);
      await s.refresh();
      assert.equal(queryCalls, 1);
      assert.equal(markCalls, 1);
      assert.match(s.fields.groupUnreadStatus.textContent, /显式刷新重试/);
      assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
      await s.refresh();
      assert.equal(queryCalls, 2);
      assert.equal(s.fields.groupUnreadCount.textContent, '0');
      assert.equal(s.fields.btnGroupReadLoaded.disabled, false, 'zero unread does not silently clear a pending explicit confirmation');
      await s.mark();
      assertMark(s.marks()[1], [oldID, lateID]);
      assert.deepEqual(s.marks()[1].init, s.marks()[0].init);
      assert.equal(s.histories().length, 1, 'failure never changes or automatically reloads the history page');
      assert.equal(markCalls, 2);
      assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
    });
  }
});

test('failed or invalid restart preserves both the old pagination position and its loaded read target', async t => {
  for (const cursor of [oldID, '0']) {
    for (const failure of ['HTTP 503', 'invalid cursor']) {
      await t.test(cursor + '/' + failure, async () => {
        let historyCalls = 0;
        const s = page(async (url, init) => {
          if (url.endsWith('/unread')) return response(unread('7'));
          if (url.endsWith('/read')) return response(unread('7', JSON.parse(init.body).message_ids));
          if (++historyCalls === 1) return response(history([firstIDs[0]], cursor));
          if (historyCalls === 2) return failure === 'HTTP 503' ? response(null, 503)
            : response({ code: 0, data: { messages: [message(newID)], next_before_message_id: 123 } });
          return response(history([oldID]));
        });
        await s.older();
        await s.refresh();
        await s.restart();
        assert.equal(s.histories().length, 2);
        assert.equal(s.histories()[1].url.endsWith('/messages?limit=20'), true, 'restart ignores the old cursor');
        assert.equal(s.fields.groupUnreadCount.textContent, '7');
        assert.equal(s.fields.btnGroupReadLoaded.disabled, false);
        assert.equal(s.rendered(newID).length, 0, 'invalid page must not render its contents');
        assert.equal(s.marks().length, 0);
        await s.mark();
        assertMark(s.marks()[0], [firstIDs[0]]);
        await s.older();
        if (cursor === '0') assert.equal(s.histories().length, 2, 'old finished state survives');
        else {
          assert.equal(s.histories().length, 3);
          assert.equal(new URL(s.histories()[2].url, 'http://gateway.test').searchParams.get('before_message_id'), cursor);
        }
      });
    }
  }
});

// Pausing json() matters: the scope can change after successful HTTP headers.
function pausedResponse(stage, gate, entered, body) {
  if (stage === 'fetch') {
    entered.resolve();
    return gate.promise.then(() => response(body));
  }
  return { ok: true, status: 200, json: async () => { entered.resolve(); await gate.promise; return body; } };
}

function changeAwayAndBack(s, field) {
  const original = s.fields[field].value;
  s.change(field, field === 'token' ? 'recovery-token-B' : '9007199254740999');
  s.change(field, original);
}

test('late history headers or body cannot revive read targets after Token or group A to B to A', async t => {
  for (const field of ['token', 'toUserId']) {
    for (const stage of ['fetch', 'body']) {
      await t.test(field + '/' + stage, async () => {
        const gate = deferred(), entered = deferred();
        let histories = 0;
        const s = page(async (url, init) => {
          if (url.endsWith('/unread')) return response(unread('9'));
          if (url.endsWith('/read')) return response(unread('8', JSON.parse(init.body).message_ids));
          if (++histories === 1) return response(history([oldID], oldID));
          if (histories === 2) return pausedResponse(stage, gate, entered, history([lateID], '0'));
          return response(history([newID]));
        });
        await s.older();
        const pending = s.restart();
        await entered.promise;
        changeAwayAndBack(s, field);
        await s.refresh();
        assert.equal(s.fields.groupUnreadCount.textContent, '9');
        gate.resolve();
        await pending;
        assert.equal(s.fields.groupUnreadCount.textContent, '9');
        assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
        assert.equal(s.rendered(lateID).length, 0, 'stale history must not appear as current');
        await s.mark();
        assert.equal(s.marks().length, 0);
        await s.older();
        assert.equal(new URL(s.histories().at(-1).url, 'http://gateway.test').searchParams.get('before_message_id'), oldID, 'stale restart must not replace the old cursor');
        await s.mark();
        assertMark(s.marks()[0], [newID]);
      });
    }
  }
});

test('late Mark or GET responses cannot clear a newly loaded page after Token or group A to B to A', async t => {
  for (const field of ['token', 'toUserId']) {
    for (const stage of ['fetch', 'body']) {
      for (const operation of ['Mark', 'GET']) {
        await t.test(field + '/' + stage + '/' + operation, async () => {
          const gate = deferred(), entered = deferred();
          let histories = 0, marks = 0, queries = 0;
          const s = page(async (url, init) => {
            if (url.includes('/messages?')) return response(history([++histories === 1 ? oldID : newID]));
            if (url.endsWith('/read')) {
              const ids = JSON.parse(init.body).message_ids;
              if (++marks === 1 && operation === 'Mark') return pausedResponse(stage, gate, entered, unread('0', ids));
              return response(unread('8', ids));
            }
            if (++queries === 1 && operation === 'GET') return pausedResponse(stage, gate, entered, unread('0'));
            return response(unread('9'));
          });
          await s.older();
          const pending = operation === 'Mark' ? s.mark() : s.refresh();
          await entered.promise;
          changeAwayAndBack(s, field);
          await s.latest();
          await s.refresh();
          assert.equal(s.fields.groupUnreadCount.textContent, '9');
          gate.resolve();
          await pending;
          assert.equal(s.fields.groupUnreadCount.textContent, '9', 'late old count must not overwrite the current count');
          assert.equal(s.fields.btnGroupReadLoaded.disabled, false, 'late old response must not clear the newer page');
          await s.mark();
          assertMark(s.marks().at(-1), [newID]);
          if (operation === 'Mark') assertMark(s.marks()[0], [oldID]);
          assert.equal(s.fields.groupUnreadCount.textContent, '8');
          assert.equal(s.fields.btnGroupReadLoaded.disabled, true);
        });
      }
    }
  }
});
