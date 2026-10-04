const test = require('node:test');
const assert = require('node:assert/strict');
const { page, draft, collection, itemData, reply, deferred, plain } = require('./multi-draft-test-helper.cjs');

const scripts = ['multi-draft-core.js', 'multi-draft-actions.js'];
const runID = collection().run_id;
const path = index => '/api/v1/agent/runs/' + runID + '/drafts/' + index;
const copy = value => JSON.parse(JSON.stringify(value));

function changed(item, fields = {}, properties = {}) {
  return { ...copy(item), ...properties, draft: { ...copy(item.draft), ...fields } };
}

function unresolved(index = 0) {
  const item = draft(index);
  item.draft.assignee_name = '李四';
  item.draft.assignee_resolution = 'ambiguous';
  item.draft.deadline = { ...item.draft.deadline, text: '明天下午', source: 'instruction',
    reference_unix_ms: item.draft.deadline.instruction_reference_unix_ms,
    resolution: 'needs_input', reason: 'unsupported_expression' };
  return item;
}

function created(index = 0, replyStatus = 'pending') {
  return changed(draft(index), {}, { status: 'succeeded', task_id: '9007199254741099', reply_status: replyStatus,
    reply_msg_id: ['pending', 'accepted'].includes(replyStatus) ? 'bot-task:' + runID + (index ? ':' + index : '') : '' });
}

test('three item saves are independent, use exact new routes and preserve unsaved fields', async () => {
  const items = [draft(0), draft(1)], requests = [];
  const harness = page(async (url, init = {}) => {
    requests.push({ url, init });
    if (url.includes('/members?')) return reply({ members: [{ user_id: '501', username: 'member', nickname: 'Teammate' }], next_after_user_id: '0' });
    if (init.method !== 'PUT') return reply(collection(copy(items)));
    const body = JSON.parse(init.body);
    assert.equal(init.headers.Authorization, 'Bearer test-token');
    if (url === path(0)) {
      assert.deepEqual(body, { title: 'New task', description: 'New details', expected_revision: '1' });
      items[0] = changed(items[0], { title: body.title, description: body.description, revision: '2' });
    } else if (url === path(0) + '/assignee') {
      assert.deepEqual(body, { assignee_id: '501', expected_revision: '2' });
      items[0] = changed(items[0], { assignee_id: '501', assignee_resolution: 'selected', revision: '3' });
    } else if (url === path(0) + '/deadline') {
      assert.deepEqual(body, { due_at_unix_ms: Date.UTC(2026, 9, 4, 7, 30, 1, 987), expected_revision: '3' });
      items[0] = changed(items[0], { due_at_unix_ms: body.due_at_unix_ms, deadline: { ...items[0].draft.deadline, resolution: 'selected' }, revision: '4' });
    } else assert.fail('unexpected write route ' + url);
    return reply(itemData(copy(items[0])));
  }, scripts);
  const controller = harness.controller();
  await controller.load(runID);
  await controller.loadMembers();
  const other = plain(controller.items[1]);
  controller.setEdit(1, { title: 'Other unsaved input' });
  controller.setEdit(0, { title: '  New task  ', description: '  New details  ', assigneeID: '501', dueInput: '2026-10-04T15:30:01.987' });
  await controller.saveText(0);
  assert.equal(controller.edits.get(0).title, 'New task');
  assert.equal(controller.edits.get(0).description, 'New details');
  assert.equal(controller.edits.get(0).assigneeID, '501');
  assert.equal(controller.edits.get(0).dueInput, '2026-10-04T15:30:01.987');
  assert.equal(controller.dirty(0), true);
  await controller.saveAssignee(0);
  assert.equal(controller.dirty(0), true);
  await controller.saveDeadline(0);
  assert.equal(controller.dirty(0), false);
  assert.deepEqual(plain(controller.items[1]), other);
  assert.equal(controller.edits.get(1).title, 'Other unsaved input');
  assert.equal(controller.items[0].draft.revision, '4');
  assert.equal(requests.filter(r => r.init.method === 'PUT').length, 3);
  assert.equal(controller.reviewedReplies.has(0), false);
});

test('explicit unassigned and deadline clearing resolve ambiguity and increment state-only changes', async () => {
  let item = unresolved(0), writes = 0;
  const originalEvidence = copy(item.draft.deadline);
  const { controller: make } = page(async (url, init = {}) => {
    if (init.method !== 'PUT') return reply(collection([copy(item), draft(1)]));
    writes++;
    const body = JSON.parse(init.body);
    if (url.endsWith('/assignee')) {
      assert.deepEqual(body, { assignee_id: '0', expected_revision: '1' });
      item = changed(item, { assignee_resolution: 'unassigned', revision: '2' });
    } else {
      assert.equal(url, path(0) + '/deadline');
      assert.deepEqual(body, { due_at_unix_ms: 0, expected_revision: '2' });
      item = changed(item, { deadline: { ...item.draft.deadline, resolution: 'unset' }, revision: '3' });
    }
    return reply(itemData(copy(item)));
  }, scripts);
  const controller = make();
  await controller.load(runID);
  controller.setEdit(0, { assigneeID: '0' });
  await controller.saveAssignee(0);
  await controller.saveDeadline(0);
  assert.equal(controller.items[0].draft.assignee_name, '李四');
  assert.equal(controller.items[0].draft.assignee_resolution, 'unassigned');
  assert.deepEqual(plain(controller.items[0].draft.deadline), { ...originalEvidence, resolution: 'unset' });
  assert.equal(controller.dirty(0), false);
  assert.equal(writes, 2);
});

test('no-op saves keep versions while matched or parsed manual selections increment versions', async () => {
  for (const mode of ['text same', 'assignee same', 'deadline same', 'matched choice', 'parsed choice']) {
    let item = draft(0);
    if (mode === 'assignee same') item.draft.assignee_resolution = 'unassigned';
    if (mode === 'deadline same') item.draft.deadline.resolution = 'unset';
    if (mode === 'matched choice') Object.assign(item.draft, { assignee_id: '501', assignee_name: 'Teammate', assignee_resolution: 'matched' });
    if (mode === 'parsed choice') {
      item.draft.due_at_unix_ms = Date.UTC(2026, 9, 4, 7, 30, 1, 987);
      item.draft.deadline = { ...item.draft.deadline, text: '明天15:30', source: 'instruction',
        reference_unix_ms: item.draft.deadline.instruction_reference_unix_ms, resolution: 'parsed', parsed_unix_ms: item.draft.due_at_unix_ms };
    }
    const { controller: make } = page(async (url, init = {}) => {
      if (init.method !== 'PUT') return reply(collection([copy(item), draft(1)]));
      if (mode.includes('choice')) item.draft.revision = '2';
      if (url.endsWith('/assignee')) item.draft.assignee_resolution = 'unassigned';
      if (mode === 'matched choice') item.draft.assignee_resolution = 'selected';
      if (url.endsWith('/deadline')) item.draft.deadline.resolution = mode === 'parsed choice' ? 'selected' : 'unset';
      return reply(itemData(copy(item)));
    }, scripts);
    const controller = make();
    await controller.load(runID);
    controller.membersLoaded = true;
    controller.members.set('501', { user_id: '501', username: 'member', nickname: 'Teammate' });
    if (mode.startsWith('text')) await controller.saveText(0);
    else if (mode.startsWith('assignee') || mode.startsWith('matched')) await controller.saveAssignee(0);
    else await controller.saveDeadline(0);
    assert.equal(controller.items[0].draft.revision, mode.includes('choice') ? '2' : '1', mode);
  }
});

test('positive assignees need the explicitly loaded directory and invalid time or text sends no write', async () => {
  let calls = 0;
  const { controller: make } = page(async () => { calls++; return reply(collection()); }, scripts);
  const controller = make();
  await controller.load(runID);
  controller.setEdit(0, { assigneeID: '501' });
  await assert.rejects(controller.saveAssignee(0), /loaded team directory/);
  controller.membersLoaded = true;
  await assert.rejects(controller.saveAssignee(0), /loaded team directory/);
  controller.setEdit(0, { assigneeID: '01' });
  await assert.rejects(controller.saveAssignee(0), /loaded team directory/);
  for (const title of ['', ' ', 'x'.repeat(201), '\ud800']) {
    controller.setEdit(0, { title });
    await assert.rejects(controller.saveText(0), /allowed length/);
  }
  for (const dueInput of ['2026-02-30T15:30', '1991-04-14T02:30', '1991-09-15T01:30', 'not a date']) {
    controller.setEdit(0, { dueInput });
    await assert.rejects(controller.saveDeadline(0), /invalid.*Shanghai/i);
  }
  assert.equal(calls, 1);
  assert.equal(controller.reload.size, 0);
});

test('409, lost response and malformed successful edits only require the target to reload', async () => {
  for (const failure of ['409', 'lost', 'scope', 'count', 'index', 'title', 'source', 'assignee', 'evidence', 'version too old', 'version jumped', 'no-op increment']) {
    const initial = collection([created(0, 'accepted'), draft(1)]);
    const response = changed(initial.items[1], { title: failure === 'no-op increment' ? 'Task 1' : 'New title', revision: '2' });
    const data = itemData(response);
    const { controller: make } = page(async (_url, init = {}) => {
      if (init.method !== 'PUT') return reply(copy(initial));
      if (failure === '409') return reply({}, 409);
      if (failure === 'lost') throw new Error('response lost');
      switch (failure) {
        case 'scope': data.group_id = '301'; break;
        case 'count': data.item_count = 1; break;
        case 'index': data.item.item_index = 0; break;
        case 'title': data.item.draft.title = 'Wrong title'; break;
        case 'source': data.item.draft.source_message_id = '99'; break;
        case 'assignee': data.item.draft.assignee_resolution = 'unassigned'; break;
        case 'evidence': data.item.draft.deadline.instruction_reference_unix_ms++; break;
        case 'version too old': data.item.draft.revision = '1'; break;
        case 'version jumped': data.item.draft.revision = '3'; break;
      }
      return reply(data);
    }, scripts);
    const controller = make();
    await controller.load(runID);
    const other = plain(controller.items[0]);
    controller.setEdit(1, { title: failure === 'no-op increment' ? 'Task 1' : 'New title' });
    await assert.rejects(controller.saveText(1));
    assert.deepEqual([...controller.reload], [1], failure);
    assert.deepEqual(plain(controller.items[0]), other, failure);
    assert.equal(controller.items[1].draft.title, 'Task 1');
    assert.equal(controller.edits.get(1).title, failure === 'no-op increment' ? 'Task 1' : 'New title');
    assert.equal(controller.busy, false);
  }
});

test('double click does not duplicate a write and newer dirty inputs survive its response', async () => {
  const pending = deferred();
  let writes = 0;
  const { controller: make } = page(async (_url, init = {}) => {
    if (init.method !== 'PUT') return reply(collection());
    writes++;
    return pending.promise;
  }, scripts);
  const controller = make();
  await controller.load(runID);
  controller.setEdit(0, { title: '  Submitted  ', description: '  Description  ' });
  const operation = controller.saveText(0);
  await assert.rejects(controller.saveText(0), /in progress/);
  controller.setEdit(0, { title: 'Newer local input', dueInput: '2026-10-04T15:30' });
  pending.resolve(reply(itemData(changed(draft(0), { title: 'Submitted', description: 'Description', revision: '2' }))));
  await operation;
  assert.equal(writes, 1);
  assert.equal(controller.items[0].draft.title, 'Submitted');
  assert.equal(controller.edits.get(0).title, 'Newer local input');
  assert.equal(controller.edits.get(0).description, 'Description');
  assert.equal(controller.edits.get(0).dueInput, '2026-10-04T15:30');
  assert.equal(controller.dirty(0), true);
});

test('confirmation sends six exact review fields and pending or unknown replies require GET before retry', async () => {
  for (const status of ['pending', 'unknown', 'not_started', 'accepted']) {
    const calls = [], initial = collection();
    let item = copy(initial.items[0]);
    const { controller: make } = page(async (url, init = {}) => {
      calls.push({ url, init });
      if (init.method !== 'POST') {
        if (url === path(0)) {
          if (item.reply_status === 'unknown') {
            item.reply_status = 'pending';
            item.reply_msg_id = 'bot-task:' + runID;
          }
          return reply(itemData(copy(item)));
        }
        return reply(copy(initial));
      }
      if (url === path(0) + '/confirm') {
        assert.deepEqual(JSON.parse(init.body), { expected_title: 'Task 0', expected_description: '', expected_revision: '1',
          expected_assignee_id: '0', expected_due_at_unix_ms: 0, expected_deadline_resolution: 'none' });
        item = created(0, status);
      } else {
        assert.equal(url, path(0) + '/reply/retry');
        assert.equal(init.body, undefined);
        assert.equal(init.headers['Content-Type'], undefined);
        assert.equal(init.headers.Authorization, 'Bearer test-token');
        item.reply_status = 'accepted';
        item.reply_msg_id = 'bot-task:' + runID;
      }
      return reply(itemData(copy(item)));
    }, scripts);
    const controller = make();
    await controller.load(runID);
    controller.setEdit(1, { title: 'Other unsaved item' });
    await controller.confirm(0);
    assert.equal(controller.items[0].status, 'succeeded');
    assert.equal(controller.items[0].task_id, '9007199254741099');
    assert.equal(controller.items[0].reply_status, status);
    assert.equal(controller.edits.get(1).title, 'Other unsaved item');
    assert.equal(controller.reviewedReplies.has(0), false);
    await assert.rejects(controller.confirm(0), /unavailable/);
    await assert.rejects(controller.retryReply(0), /Load.*review/);
    await controller.loadItem(0);
    if (status === 'accepted') await assert.rejects(controller.retryReply(0), /Load.*review/);
    else {
      await controller.retryReply(0);
      assert.equal(controller.items[0].reply_status, 'accepted');
      assert.equal(controller.items[0].task_id, '9007199254741099');
      assert.match(controller.message, /Delivery.*not confirmed/);
    }
    assert.equal(calls.filter(call => call.url.endsWith('/confirm')).length, 1);
    assert.equal(calls.filter(call => call.url.endsWith('/reply/retry')).length, status === 'accepted' ? 0 : 1);
  }
});

test('confirmation blocks unsaved or unresolved target fields while another dirty item is independent', async () => {
  for (const mode of ['title', 'description', 'assignee', 'deadline', 'ambiguous', 'needs input', 'reload', 'creating not reviewed']) {
    let item = draft(0);
    if (mode === 'ambiguous' || mode === 'needs input') {
      item = unresolved(0);
      if (mode === 'ambiguous') item.draft.deadline = draft(0).draft.deadline;
      else {
        item.draft.assignee_name = '';
        item.draft.assignee_resolution = 'none';
      }
    }
    if (mode === 'creating not reviewed') item.status = 'creating';
    let calls = 0;
    const { controller: make } = page(async () => { calls++; return reply(collection([item, draft(1)])); }, scripts);
    const controller = make();
    await controller.load(runID);
    if (mode === 'title') controller.setEdit(0, { title: 'New title' });
    if (mode === 'description') controller.setEdit(0, { description: 'New description' });
    if (mode === 'assignee') controller.setEdit(0, { assigneeID: '501' });
    if (mode === 'deadline') controller.setEdit(0, { dueInput: '2026-10-04T15:30' });
    if (mode === 'reload') controller.reload.add(0);
    if (mode === 'creating not reviewed') controller.reviewedReplies.delete(0);
    await assert.rejects(controller.confirm(0));
    assert.equal(calls, 1, mode);
  }
});

test('uncertain creation requires an explicit item GET, then retrying creating uses its frozen snapshot', async () => {
  let calls = 0, phase = 'lost';
  const original = draft(0), creating = changed(original, {}, { status: 'creating' });
  const { controller: make } = page(async (url, init = {}) => {
    if (init.method !== 'POST') return reply(url === path(0) ? itemData(creating) : collection([original, draft(1)]));
    assert.equal(url, path(0) + '/confirm');
    assert.deepEqual(JSON.parse(init.body), { expected_title: original.draft.title, expected_description: '', expected_revision: '1',
      expected_assignee_id: '0', expected_due_at_unix_ms: 0, expected_deadline_resolution: 'none' });
    calls++;
    if (phase === 'lost') throw new Error('response lost');
    return reply(itemData(created(0, 'pending')));
  }, scripts);
  const controller = make();
  await controller.load(runID);
  await assert.rejects(controller.confirm(0), /response lost/);
  await assert.rejects(controller.confirm(0), /Load/);
  assert.equal(calls, 1);
  await controller.loadItem(0);
  assert.equal(controller.items[0].status, 'creating');
  await assert.rejects(controller.saveText(0), /unavailable/);
  await assert.rejects(controller.skip(0), /unavailable/);
  phase = 'saved';
  await controller.confirm(0);
  assert.equal(controller.items[0].status, 'succeeded');
  assert.equal(controller.items[0].task_id, '9007199254741099');
  assert.equal(calls, 2);
});

test('bad confirmation responses do not replace the target or erase another saved Task', async () => {
  for (const change of ['revision', 'description', 'assignee name', 'deadline evidence', 'task zero', 'reply wrong item', 'status creating']) {
    const initial = collection([created(0, 'accepted'), draft(1)]);
    const result = created(1, 'pending');
    switch (change) {
      case 'revision': result.draft.revision = '2'; break;
      case 'description': result.draft.description = 'Not reviewed'; break;
      case 'assignee name': result.draft.assignee_name = 'Not reviewed'; result.draft.assignee_resolution = 'unassigned'; break;
      case 'deadline evidence': result.draft.deadline.instruction_reference_unix_ms++; break;
      case 'task zero': result.task_id = '0'; break;
      case 'reply wrong item': result.reply_msg_id = 'bot-task:' + runID; break;
      case 'status creating': result.status = 'creating'; result.task_id = '0'; result.reply_status = 'not_started'; result.reply_msg_id = ''; break;
    }
    const { controller: make } = page(async (_url, init = {}) => reply(init.method === 'POST' ? itemData(result) : initial), scripts);
    const controller = make();
    await controller.load(runID);
    const other = plain(controller.items[0]);
    await assert.rejects(controller.confirm(1), /Invalid/);
    assert.deepEqual(plain(controller.items[0]), other, change);
    assert.deepEqual([...controller.reload], [1], change);
    assert.equal(controller.items[1].status, 'waiting_confirmation');
  }
});

test('skip preserves unresolved evidence and max revision, and never creates a Task', async () => {
  const item = unresolved(0);
  item.draft.revision = '9223372036854775807';
  const original = copy(item), other = created(1, 'accepted'), calls = [];
  const { controller: make } = page(async (url, init = {}) => {
    calls.push({ url, init });
    if (init.method !== 'POST') return reply(collection([copy(item), other]));
    assert.equal(url, path(0) + '/skip');
    assert.deepEqual(JSON.parse(init.body), { expected_revision: '9223372036854775807' });
    return reply(itemData(changed(item, {}, { status: 'skipped', reply_status: 'disabled' })));
  }, scripts);
  const controller = make();
  await controller.load(runID);
  await controller.skip(0);
  assert.deepEqual(plain(controller.items[0].draft), original.draft);
  assert.equal(controller.items[0].task_id, '0');
  assert.equal(controller.items[0].reply_status, 'disabled');
  assert.equal(controller.items[0].reply_msg_id, '');
  assert.deepEqual(plain(controller.items[1]), other);
  await assert.rejects(controller.skip(0), /unavailable/);
  await assert.rejects(controller.confirm(0), /unavailable/);
  await assert.rejects(controller.saveAssignee(0), /unavailable/);
  assert.equal(calls.filter(call => call.init.method === 'POST').length, 1);
});

test('skip blocks any dirty input and creating or succeeded items; malformed skip needs target reload', async () => {
  for (const mode of ['title', 'description', 'assignee', 'deadline', 'creating', 'succeeded', 'changed version', 'changed content']) {
    let item = draft(0), calls = 0;
    if (mode === 'creating') item.status = 'creating';
    if (mode === 'succeeded') item = created(0, 'accepted');
    const { controller: make } = page(async (_url, init = {}) => {
      if (init.method !== 'POST') return reply(collection([item, draft(1)]));
      calls++;
      return reply(itemData(changed(item, mode === 'changed version' ? { revision: '2' } : { description: 'Changed' }, { status: 'skipped', reply_status: 'disabled' })));
    }, scripts);
    const controller = make();
    await controller.load(runID);
    if (mode === 'title') controller.setEdit(0, { title: 'Unsaved' });
    if (mode === 'description') controller.setEdit(0, { description: 'Unsaved' });
    if (mode === 'assignee') controller.setEdit(0, { assigneeID: '501' });
    if (mode === 'deadline') controller.setEdit(0, { dueInput: '2026-10-04T15:30' });
    await assert.rejects(controller.skip(0));
    assert.equal(calls, mode.startsWith('changed') ? 1 : 0, mode);
    assert.equal(controller.reload.has(0), mode.startsWith('changed'), mode);
  }
});

test('reply retries preserve saved Task on errors and send only the original card after item GET', async () => {
  for (const failure of ['409', 'lost', 'wrong task', 'wrong message', 'pending result', 'changed snapshot']) {
    const initial = collection([created(0, 'accepted'), created(1, 'pending')]), calls = [];
    let recovered = false;
    const { controller: make } = page(async (url, init = {}) => {
      calls.push({ url, init });
      if (init.method !== 'POST') return reply(url === path(1) ? itemData(copy(initial.items[1])) : copy(initial));
      assert.equal(url, path(1) + '/reply/retry');
      assert.equal(init.body, undefined);
      if (!recovered && failure === '409') return reply({}, 409);
      if (!recovered && failure === 'lost') throw new Error('reply response lost');
      const item = changed(initial.items[1], {}, { reply_status: 'accepted' });
      if (!recovered) {
        if (failure === 'wrong task') item.task_id = '9007199254741100';
        if (failure === 'wrong message') item.reply_msg_id = 'bot-task:' + runID;
        if (failure === 'pending result') item.reply_status = 'pending';
        if (failure === 'changed snapshot') item.draft.title = 'Not the fixed card';
      }
      return reply(itemData(item));
    }, scripts);
    const controller = make();
    await controller.load(runID);
    const before = plain(controller.items);
    await assert.rejects(controller.retryReply(1));
    assert.deepEqual(plain(controller.items), before, failure);
    assert.deepEqual([...controller.reload], [1], failure);
    assert.equal(controller.reviewedReplies.has(1), false);
    await assert.rejects(controller.retryReply(1), /Load/);
    await assert.rejects(controller.confirm(1), /Load/);
    recovered = true;
    await controller.loadItem(1);
    await controller.retryReply(1);
    assert.equal(controller.items[1].task_id, before[1].task_id);
    assert.equal(controller.items[1].reply_status, 'accepted');
    assert.equal(calls.filter(call => call.init.method === 'POST').length, 2);
    assert.equal(calls.some(call => call.url.endsWith('/confirm')), false);
  }
});

test('old success or failure responses cannot write into changed Token or team-group context', async () => {
  for (const mode of ['token success', 'group success', 'team failure']) {
    const pending = deferred();
    const harness = page(async (_url, init = {}) => {
      if (init.method === 'PUT') return pending.promise;
      return reply(collection(undefined, { team_id: harness.fields.teamId.value, group_id: harness.fields.toUserId.value }));
    }, scripts);
    const controller = harness.controller();
    await controller.load(runID);
    controller.setEdit(0, { title: 'Submitted in old context' });
    const operation = controller.saveText(0);
    if (mode.startsWith('token')) harness.fields.token.value = 'different-token';
    if (mode.startsWith('group')) harness.fields.toUserId.value = '301';
    if (mode.startsWith('team')) harness.fields.teamId.value = '201';
    controller.invalidate();
    await controller.load(runID);
    const current = plain(controller.items);
    const scope = plain(controller.scope);
    if (mode.endsWith('failure')) pending.reject(new Error('old request failed'));
    else pending.resolve(reply(itemData(changed(draft(0), { title: 'Submitted in old context', revision: '2' }))));
    await operation;
    assert.deepEqual(plain(controller.items), current, mode);
    assert.deepEqual(plain(controller.scope), scope, mode);
    assert.equal(controller.reload.size, 0);
    assert.equal(controller.busy, false);
  }
});

test('assignee and deadline saves preserve newer input for that field and other unsaved fields', async () => {
  for (const mode of ['assignee', 'deadline']) {
    const pending = deferred();
    const { controller: make } = page(async (_url, init = {}) => init.method === 'PUT' ? pending.promise : reply(collection()), scripts);
    const controller = make();
    await controller.load(runID);
    controller.membersLoaded = true;
    controller.members.set('501', { user_id: '501', username: 'one', nickname: '' });
    controller.members.set('502', { user_id: '502', username: 'two', nickname: '' });
    controller.setEdit(0, { title: 'Unsaved text', assigneeID: '501', dueInput: '2026-10-04T15:30' });
    const operation = mode === 'assignee' ? controller.saveAssignee(0) : controller.saveDeadline(0);
    if (mode === 'assignee') controller.setEdit(0, { assigneeID: '502' });
    else controller.setEdit(0, { dueInput: '2026-10-04T16:30' });
    const item = changed(draft(0), mode === 'assignee' ? { assignee_id: '501', assignee_resolution: 'selected', revision: '2' } :
      { due_at_unix_ms: Date.UTC(2026, 9, 4, 7, 30), deadline: { ...draft(0).draft.deadline, resolution: 'selected' }, revision: '2' });
    pending.resolve(reply(itemData(item)));
    await operation;
    assert.equal(controller.edits.get(0).title, 'Unsaved text');
    assert.equal(controller.edits.get(0).assigneeID, mode === 'assignee' ? '502' : '501');
    assert.equal(controller.edits.get(0).dueInput, mode === 'deadline' ? '2026-10-04T16:30' : '2026-10-04T15:30');
    assert.equal(controller.dirty(0), true);
  }
});

test('manual state saves reject wrong versions, resolution, names or preserved deadline evidence', async () => {
  for (const mode of ['assignee version', 'assignee state', 'assignee name', 'deadline version', 'deadline state', 'deadline evidence']) {
    const item = changed(draft(0), { revision: mode.endsWith('version') ? '1' : '2' });
    if (mode.startsWith('assignee')) item.draft.assignee_resolution = mode.endsWith('state') ? 'none' : 'unassigned';
    if (mode.endsWith('name')) item.draft.assignee_name = 'Changed name';
    if (mode.startsWith('deadline')) item.draft.deadline.resolution = mode.endsWith('state') ? 'none' : 'unset';
    if (mode.endsWith('evidence')) item.draft.deadline.instruction_reference_unix_ms++;
    const { controller: make } = page(async (_url, init = {}) => reply(init.method === 'PUT' ? itemData(item) : collection()), scripts);
    const controller = make();
    await controller.load(runID);
    await assert.rejects(mode.startsWith('assignee') ? controller.saveAssignee(0) : controller.saveDeadline(0), /Invalid/);
    assert.deepEqual([...controller.reload], [0]);
    assert.equal(controller.items[0].draft.revision, '1');
    assert.equal(controller.items[1].draft.revision, '1');
  }
});

test('a saved deadline normalizes unchanged local seconds and fractional input without leaving false dirty state', async () => {
  const due = Date.UTC(2026, 9, 4, 7, 30);
  const { controller: make } = page(async (_url, init = {}) => {
    if (init.method !== 'PUT') return reply(collection());
    assert.equal(JSON.parse(init.body).due_at_unix_ms, due);
    return reply(itemData(changed(draft(0), { revision: '2', due_at_unix_ms: due, deadline: { ...draft(0).draft.deadline, resolution: 'selected' } })));
  }, scripts);
  const controller = make();
  await controller.load(runID);
  controller.setEdit(0, { dueInput: '2026-10-04T15:30:00.000' });
  await controller.saveDeadline(0);
  assert.equal(controller.edits.get(0).dueInput, '2026-10-04T15:30');
  assert.equal(controller.dirty(0), false);
});
