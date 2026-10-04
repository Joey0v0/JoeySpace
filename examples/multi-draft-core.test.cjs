const test = require('node:test');
const assert = require('node:assert/strict');
const { page, draft, collection, itemData, reply, deferred, plain } = require('./multi-draft-test-helper.cjs');

const runID = '9007199254741011';
const succeed = (index, replyStatus = 'pending') => draft(index, { status: 'succeeded', task_id: '9007199254740993',
  reply_status: replyStatus, reply_msg_id: ['pending', 'accepted'].includes(replyStatus) ? 'bot-task:' + runID + (index ? ':' + index : '') : '' });

test('collection reads accept one and five independent items with exact large IDs', async () => {
  for (const count of [1, 5]) {
    const data = collection(Array.from({ length: count }, (_, index) => draft(index)));
    const { controller } = page(async (path, init) => {
      assert.equal(path, '/api/v1/agent/runs/' + runID + '/drafts');
      assert.equal(init.headers.Authorization, 'Bearer test-token');
      assert.equal(init.headers['Content-Type'], undefined);
      return reply(data);
    }, ['multi-draft-core.js']);
    const c = controller();
    await c.load(runID);
    assert.deepEqual(plain(c.items), data.items);
    assert.equal(c.reviewedReplies.size, count);
    assert.equal(c.busy, false);
  }
});

test('strict validation rejects damaged final items without partially accepting a collection', async () => {
  for (const mutate of [
    data => { data.item_count = 3; },
    data => { data.items[1].item_index = 0; },
    data => { delete data.items[1]; },
    data => { data.items[1].draft.revision = '0'; },
    data => { data.items[1].draft.title = ' bad '; },
    data => { data.items[1].draft.description = '\ud800'; },
    data => { delete data.items[1].draft.deadline; },
    data => { delete data.items[1].draft.deadline.reason; },
    data => { data.items[1].draft.source_message_id = 9007199254740993; },
    data => { data.items[1].draft.assignee_resolution = ''; },
    data => { data.team_id = '201'; },
    data => { data.run_id = Number(runID); },
    data => { data.items[1] = succeed(1); data.items[1].reply_msg_id = 'bot-task:' + runID; },
    data => { data.items[1].reply_status = 'unknown'; },
  ]) {
    const data = collection(); mutate(data);
    const { controller } = page(async () => reply(data), ['multi-draft-core.js']);
    const c = controller();
    await assert.rejects(c.load(runID), /invalid collection/);
    assert.equal(c.items.length, 0);
    assert.equal(c.edits.size, 0);
  }
});

test('skipped keeps unresolved evidence while creating and succeeded cannot', () => {
  const { context } = page(async () => {}, ['multi-draft-core.js']);
  const item = draft(0, { status: 'skipped', reply_status: 'disabled' });
  item.draft.assignee_name = '李四'; item.draft.assignee_resolution = 'ambiguous';
  item.draft.deadline = { text: '明天下午', source: 'instruction', source_message_id: '0', reference_unix_ms: 123,
    timezone: 'Asia/Shanghai', resolution: 'needs_input', reason: 'unsupported_expression', parsed_unix_ms: 0, instruction_reference_unix_ms: 123 };
  assert.equal(context.MultiDraftController.validItem(item, runID, 0), true);
  item.status = 'creating'; item.reply_status = 'not_started';
  assert.equal(context.MultiDraftController.validItem(item, runID, 0), false);
  item.status = 'succeeded'; item.task_id = '9223372036854775807';
  assert.equal(context.MultiDraftController.validItem(item, runID, 0), false);
});

test('generation retries keep first instruction reference, key and Token across elapsed time', async () => {
  let now = 1791000000123, attempts = 0;
  const submissions = [];
  const { controller, fields } = page(async (path, init) => {
    if (init.method === 'POST') {
      submissions.push({ headers: init.headers, body: JSON.parse(init.body) });
      return ++attempts === 1 ? reply(null, 503) : reply({ run_id: runID });
    }
    return reply(collection());
  }, ['multi-draft-core.js']);
  const c = controller({ now: () => now });
  const originalKey = c.requestKey;
  await assert.rejects(c.prepare('提取待办'), /503/);
  now += 86400000;
  await c.prepare('提取待办');
  assert.equal(c.requestKey, originalKey);
  assert.equal(c.reference, 1791000000123);
  assert.deepEqual(submissions[1], submissions[0]);
  await assert.rejects(c.prepare('改指令'), /new key/);
  fields.token.value = 'new-token';
  await assert.rejects(c.prepare('提取待办'), /new key/);
  c.invalidate(); c.newRequestKey();
  await c.prepare('改指令');
  assert.equal(c.reference, now);
  assert.notEqual(c.requestKey, originalKey);
  assert.equal(submissions.at(-1).headers.Authorization, 'Bearer new-token');
  c.requestKey = 'unrecognized-key';
  await assert.rejects(c.prepare('改指令'), /unknown request key/);
});

test('a known generation run remains fixed after GET failure and manual run switching', async () => {
  const otherRun = '9007199254741012';
  let responseRun = runID, failRead = true;
  const { controller } = page(async (path, init) => {
    if (init.method === 'POST') return reply({ run_id: responseRun });
    if (failRead) return reply(null, 504);
    return reply(collection([draft(0)], { run_id: path.includes(otherRun) ? otherRun : runID }));
  }, ['multi-draft-core.js']);
  const c = controller();
  await assert.rejects(c.prepare('固定待办'), /504/);
  assert.equal(c.runID, runID);
  responseRun = otherRun; failRead = false;
  await assert.rejects(c.prepare('固定待办'), /different run/);
  assert.equal(c.runID, runID); assert.equal(c.items.length, 0);
  responseRun = runID; await c.prepare('固定待办');
  c.setEdit(0, { title: 'keep known input' });
  responseRun = otherRun;
  await assert.rejects(c.prepare('固定待办'), /different run/);
  assert.equal(c.runID, runID); assert.equal(c.edits.get(0).title, 'keep known input');
  await c.load(otherRun);
  await assert.rejects(c.prepare('固定待办'), /different run/);
  assert.equal(c.runID, otherRun);
  responseRun = runID; await c.prepare('固定待办');
  assert.equal(c.runID, runID);
});

test('GET preserves only each dirty input field, and freezing clears editable inputs', async () => {
  let data = collection();
  const { controller } = page(async () => reply(data), ['multi-draft-core.js']);
  const c = controller(); await c.load(runID);
  c.setEdit(0, { title: 'local title' });
  c.setEdit(1, { description: 'local description', assigneeID: '9007199254740993' });
  data = collection();
  data.items[0].draft.description = 'server description'; data.items[0].draft.revision = '2';
  data.items[1].draft.title = 'server title';
  await c.load(runID);
  assert.equal(c.edits.get(0).title, 'local title');
  assert.equal(c.edits.get(0).description, 'server description');
  assert.equal(c.edits.get(1).title, 'server title');
  assert.equal(c.edits.get(1).description, 'local description');
  assert.equal(c.edits.get(1).assigneeID, '9007199254740993');
  assert.equal(c.dirty(0), true); assert.equal(c.dirty(1), true);
  data = collection([succeed(0), draft(1, { status: 'skipped', reply_status: 'disabled' })]);
  await c.load(runID);
  assert.equal(c.edits.size, 0); assert.equal(c.dirty(0), false);
});

test('read failure retains known Task result and other inputs, targeted GET clears only its reload', async () => {
  let failed = false;
  const data = collection([succeed(0), draft(1)]);
  const { controller } = page(async path => {
    if (failed) return reply(null, 504);
    return path.endsWith('/1') ? reply(itemData(data.items[1])) : reply(data);
  }, ['multi-draft-core.js']);
  const c = controller(); await c.load(runID); c.setEdit(1, { title: 'keep input' });
  failed = true;
  await assert.rejects(c.load(runID), /504/);
  assert.equal(c.items[0].task_id, '9007199254740993');
  assert.equal(c.items[0].reply_status, 'pending');
  assert.equal(c.edits.get(1).title, 'keep input');
  assert.deepEqual([...c.reload], [0, 1]); assert.equal(c.reviewedReplies.size, 0);
  failed = false;
  await c.loadItem(1);
  assert.deepEqual([...c.reload], [0]);
  assert.equal(c.reviewedReplies.has(1), true);
  assert.equal(c.reviewedReplies.has(0), false);
  assert.equal(c.edits.get(1).title, 'keep input');
});

test('scope and epoch switching discard pending responses, and busy rejects a second operation', async () => {
  for (const field of ['token', 'teamId', 'toUserId']) {
    const pending = deferred();
    const { controller, fields } = page(async () => pending.promise, ['multi-draft-core.js']);
    const c = controller();
    const first = c.load(runID);
    assert.equal(c.scope.token, 'test-token');
    await assert.rejects(c.load(runID), /in progress/);
    fields[field].value = field === 'token' ? 'other-token' : '999';
    pending.resolve(reply(collection()));
    await assert.rejects(first, /context changed/);
    assert.equal(c.items.length, 0); assert.equal(c.busy, false);
  }
  const pending = deferred(); let calls = 0;
  const nextRun = '9007199254741012';
  const { controller } = page(async () => ++calls === 1 ? pending.promise : reply(collection([draft(0)], { run_id: nextRun })), ['multi-draft-core.js']);
  const c = controller(), old = c.load(runID);
  c.invalidate(); await c.load(nextRun);
  pending.resolve(reply(collection())); await assert.rejects(old, /context changed/);
  assert.equal(c.runID, nextRun); assert.equal(c.items.length, 1); assert.equal(c.busy, false);
});

test('member loading is explicit, strictly paged, and preserves prior choices on bad later page', async () => {
  let memberCalls = 0, malformed = true;
  const seen = [];
  const { controller } = page(async path => {
    if (path.endsWith('/drafts')) return reply(collection());
    seen.push(path); memberCalls++;
    if (memberCalls === 1) return reply({ members: [{ user_id: '9007199254740993', username: 'first', nickname: '甲' }], next_after_user_id: '9007199254740993' });
    return reply({ members: [{ user_id: malformed ? '9007199254740993' : '9007199254740994', username: 'next', nickname: '' }], next_after_user_id: '0' });
  }, ['multi-draft-core.js']);
  const c = controller(); await c.load(runID);
  assert.equal(memberCalls, 0);
  await c.loadMembers();
  assert.equal(c.membersLoaded, true); assert.equal(c.members.size, 1);
  await assert.rejects(c.loadMembers(true), /invalid member/);
  assert.equal(c.members.size, 1); assert.equal(c.memberCursor, '9007199254740993');
  malformed = false; await c.loadMembers(true);
  assert.equal(c.members.size, 2); assert.equal(c.memberCursor, '0');
  assert.ok(seen[1].includes('after_user_id=9007199254740993'));
  await assert.rejects(c.loadMembers(true), /no more/);
});

test('member directory rejects malformed IDs, duplicate ordering, cursor and wrong context atomically', async () => {
  const member = { user_id: '501', username: 'member', nickname: '' };
  for (const data of [
    { members: [{ ...member, user_id: 501 }], next_after_user_id: '0' },
    { members: [member, member], next_after_user_id: '0' },
    { members: [member], next_after_user_id: '502' },
    { members: [], next_after_user_id: '501' },
    { members: [{ ...member, nickname: '\ud800' }], next_after_user_id: '0' },
    { members: Array(101).fill(member), next_after_user_id: '0' },
  ]) {
    const { controller } = page(async path => reply(path.endsWith('/drafts') ? collection() : data), ['multi-draft-core.js']);
    const c = controller(); await c.load(runID);
    await assert.rejects(c.loadMembers(), /invalid member/);
    assert.equal(c.members.size, 0); assert.equal(c.membersLoaded, false);
  }
  const pending = deferred();
  const { controller, fields } = page(async path => path.endsWith('/drafts') ? reply(collection()) : pending.promise, ['multi-draft-core.js']);
  const c = controller(); await c.load(runID);
  const request = c.loadMembers(); fields.teamId.value = '999';
  pending.resolve(reply({ members: [member], next_after_user_id: '0' }));
  await assert.rejects(request, /context changed/);
  assert.equal(c.members.size, 0);
});
