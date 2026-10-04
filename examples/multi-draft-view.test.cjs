const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { page, draft, collection, itemData, reply, deferred, plain } = require('./multi-draft-test-helper.cjs');
const scripts = ['multi-draft-core.js', 'multi-draft-actions.js', 'multi-draft-view.js'];
const runID = '9007199254741011';

function boot(fetch) { return page(fetch, scripts); }
function visit(root) { return [root, ...(root.children || []).flatMap(visit)]; }
function text(root) { return visit(root).map(node => node.textContent || '').join('\n'); }
function controls(fields, index) {
  const card = fields.multiDraftItems.children.find(node => node.dataset.index === String(index));
  assert.ok(card, 'missing card ' + index);
  const result = { card };
  for (const node of visit(card)) {
    if (node.dataset.field) result[node.dataset.field] = node;
    if (node.dataset.action) result[node.dataset.action] = node;
  }
  return result;
}
async function load(fields, id = runID) {
  fields.multiDraftRunID.value = id;
  fields.multiDraftRunID.oninput();
  await fields.btnMultiLoad.onclick();
}
function edit(control, value) {
  control.value = value;
  (control.oninput || control.onchange)();
}

test('HTML contains an independent accessible panel and three same-origin scripts after the old script', () => {
  const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
  for (const id of ['multiDraftInstruction', 'multiDraftRequestKey', 'multiDraftRunID', 'multiDraftReference', 'multiDraftMessage',
    'multiDraftSummary', 'multiDraftItems', 'btnMultiPrepare', 'btnMultiLoad', 'btnMultiNewKey', 'btnMultiMembers', 'btnMultiMoreMembers']) {
    assert.equal([...html.matchAll(new RegExp('id="' + id + '"', 'g'))].length, 1, id);
  }
  assert.deepEqual([...html.matchAll(/<script src="([^"]+)"/g)].map(match => match[1]), scripts.map(name => '/demo/' + name));
  assert.ok(html.indexOf('<script src=') > html.indexOf('</script>'));
  assert.match(html, /typeof globalThis\.invalidateMultiDraftPage === 'function'/);
  assert.doesNotMatch(fs.readFileSync(path.join(__dirname, 'multi-draft-view.js'), 'utf8'), /\.innerHTML\s*=/);
});

test('page starts idle without automatically generating, reading members, confirming or posting', () => {
  const { fields } = boot(() => assert.fail('unexpected automatic fetch'));
  assert.equal(fields.multiDraftItems.children.length, 0);
  assert.equal(fields.btnMultiPrepare.disabled, false);
  assert.equal(fields.btnMultiLoad.disabled, true);
  assert.equal(fields.btnMultiMembers.disabled, true);
  assert.match(fields.multiDraftReference.textContent, /device clock/);
});

test('cards retain stable human numbering and display large IDs, saved assignee and all deadline evidence safely', async () => {
  const first = draft(0), second = draft(1);
  first.draft.title = '<img src=x onerror=alert(1)>';
  first.draft.description = '<script>alert(2)</script>';
  first.draft.revision = '9007199254740993';
  first.draft.assignee_id = '9007199254740995';
  first.draft.assignee_name = '<svg onload=alert(3)>';
  first.draft.assignee_resolution = 'matched';
  first.draft.source_message_id = '9007199254740997';
  first.draft.due_at_unix_ms = 1791180000456;
  first.draft.deadline = { text: '<img> tomorrow', source: 'message', source_message_id: '9007199254740997',
    reference_unix_ms: 1791097200123, timezone: 'Asia/Shanghai', resolution: 'parsed', reason: '',
    parsed_unix_ms: 1791180000456, instruction_reference_unix_ms: 1791097200789 };
  const { fields } = boot(async () => reply(collection([first, second])));
  await load(fields);
  const card = controls(fields, 0), contents = text(card.card);
  for (const value of ['Candidate 1', first.draft.title, first.draft.description, first.draft.revision,
    'Original assignee name: ' + first.draft.assignee_name, '#9007199254740995', first.draft.deadline.text,
    'Source: message', 'Source message ID: 9007199254740997', 'Interpretation reference:', 'Timezone: Asia/Shanghai',
    'Resolution: parsed', 'Original issue: (none)', 'Original parsed candidate:', 'First instruction reference (saved):', 'UTC:']) {
    assert.ok(contents.includes(value), value);
  }
  assert.match(text(controls(fields, 1).card), /Candidate 2/);
  assert.equal(card.dueInput.value, '2026-10-05T14:00:00.456');
  assert.equal(card.saveAssignee.disabled, true, 'unknown saved ID is displayed but is not a loaded member');
  assert.equal(card.confirm.disabled, false, 'matched saved assignee does not require directory reload to confirm');
  assert.equal(visit(card.card).filter(node => ['IMG', 'SCRIPT', 'SVG'].includes(node.tagName)).length, 0);
  assert.equal(fields.multiDraftReference.textContent.includes('Current generation request reference:'), false);
});

test('summary separates created, skipped, unresolved Task attempts and accepted versus unknown replies', async () => {
  const items = [draft(0, { status: 'succeeded', task_id: '9007199254740995', reply_status: 'accepted', reply_msg_id: 'bot-task:' + runID }),
    draft(1, { status: 'succeeded', task_id: '9007199254740997', reply_status: 'unknown' }),
    draft(2, { status: 'creating' }), draft(3, { status: 'skipped', reply_status: 'disabled' }), draft(4)];
  const { fields } = boot(async () => reply(collection(items)));
  await load(fields);
  assert.match(fields.multiDraftSummary.textContent, /waiting: 1, creation result pending: 1, created: 2, skipped: 1/);
  assert.match(fields.multiDraftSummary.textContent, /accepted by IM: 1, pending: 0, unknown: 1/);
  assert.match(fields.multiDraftSummary.textContent, /acceptance does not confirm delivery/);
  assert.match(text(controls(fields, 0).card), /delivery to group members is not confirmed/);
  assert.match(text(controls(fields, 2).card), /no background execution is running/);
  assert.equal(controls(fields, 1).retryReply.disabled, true);
  for (const index of [0, 1, 2, 3]) {
    const card = controls(fields, index);
    assert.equal(card.title.disabled, true);
    assert.equal(card.saveText.disabled, true);
    assert.equal(card.skip.disabled, true);
  }
});

test('all skipped candidates are handled without claiming a task was created or a reply accepted', async () => {
  const { fields } = boot(async () => reply(collection([draft(0, { status: 'skipped', reply_status: 'disabled' }), draft(1, { status: 'skipped', reply_status: 'disabled' })])));
  await load(fields);
  assert.match(fields.multiDraftSummary.textContent, /all skipped; no tasks created/);
  assert.match(fields.multiDraftSummary.textContent, /created: 0, skipped: 2/);
  assert.match(fields.multiDraftSummary.textContent, /accepted by IM: 0/);
  assert.equal(controls(fields, 0).confirm.disabled, true);
});

test('unresolved assignee and deadline require explicit saves, while unchanged candidates can be skipped', async () => {
  const item = draft(0);
  item.draft.assignee_name = '王五'; item.draft.assignee_resolution = 'ambiguous';
  item.draft.deadline = { text: '明天下午', source: 'instruction', source_message_id: '0', reference_unix_ms: 1791000000123,
    timezone: 'Asia/Shanghai', resolution: 'needs_input', reason: 'unsupported_expression', parsed_unix_ms: 0, instruction_reference_unix_ms: 1791000000123 };
  const calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push(url);
    if (options.method === 'POST') {
      assert.ok(url.endsWith('/0/skip'));
      assert.deepEqual(JSON.parse(options.body), { expected_revision: '1' });
      return reply(itemData({ ...item, status: 'skipped', reply_status: 'disabled' }));
    }
    return reply(collection([item, draft(1)]));
  });
  await load(fields);
  const card = controls(fields, 0);
  assert.equal(card.assigneeID.value, '');
  assert.equal(card.confirm.disabled, true);
  assert.equal(card.skip.disabled, false);
  assert.equal(card.saveDeadline.disabled, false, 'explicit saving of empty input resolves needs_input');
  await card.confirm.onclick();
  assert.equal(calls.length, 1, 'direct event also invokes actions guard');
  assert.equal(context.multiDraftPage.snapshot(0).draft.deadline.resolution, 'needs_input');
  await card.skip.onclick();
  assert.equal(calls.length, 2);
  assert.equal(context.multiDraftPage.snapshot(0).status, 'skipped');
  assert.equal(context.multiDraftPage.snapshot(0).draft.deadline.text, '明天下午');
  assert.equal(controls(fields, 1).confirm.disabled, false);
});

test('input changes reuse their card, block confirm and skip, and independent saves use item paths and versions', async () => {
  const items = [draft(0), draft(1)], calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push([url, options]);
    if (!options.method) return reply(collection(plain(items)));
    const body = JSON.parse(options.body), target = items[0];
    assert.equal(body.expected_revision, target.draft.revision);
    if (url.endsWith('/assignee')) {
      target.draft.assignee_id = body.assignee_id; target.draft.assignee_resolution = 'unassigned';
    } else if (url.endsWith('/deadline')) {
      target.draft.due_at_unix_ms = body.due_at_unix_ms; target.draft.deadline.resolution = 'unset';
    } else {
      assert.ok(url.endsWith('/drafts/0')); target.draft.title = body.title; target.draft.description = body.description;
    }
    target.draft.revision = String(BigInt(target.draft.revision) + 1n);
    return reply(itemData(plain(target)));
  });
  await load(fields);
  const card = controls(fields, 0);
  edit(card.title, 'Renamed task');
  edit(card.description, 'Saved details');
  assert.equal(controls(fields, 0).title, card.title, 'typing does not recreate the focused input');
  assert.equal(card.confirm.disabled, true); assert.equal(card.skip.disabled, true);
  await card.confirm.onclick(); await card.skip.onclick();
  assert.equal(calls.length, 1);
  await card.saveText.onclick();
  assert.equal(context.multiDraftPage.snapshot(0).draft.revision, '2');
  assert.equal(card.confirm.disabled, false);
  await card.saveAssignee.onclick();
  assert.equal(context.multiDraftPage.snapshot(0).draft.assignee_resolution, 'unassigned');
  await card.saveDeadline.onclick();
  assert.equal(context.multiDraftPage.snapshot(0).draft.deadline.resolution, 'unset');
  assert.equal(context.multiDraftPage.snapshot(0).draft.revision, '4');
  assert.equal(context.multiDraftPage.snapshot(1).draft.revision, '1');
  assert.equal(calls.slice(1).every(([url, options]) => url.includes('/drafts/0') && options.method === 'PUT'), true);
});

test('member pages load only on explicit clicks and selecting page two saves the real large member ID', async () => {
  const item = draft(0), calls = [];
  item.draft.assignee_name = '同名'; item.draft.assignee_resolution = 'ambiguous';
  const firstID = '9007199254740993', secondID = '9007199254740995';
  const { context, fields } = boot(async (url, options) => {
    calls.push(url);
    if (url.includes('/members?after_user_id=0')) return reply({ members: [{ user_id: firstID, username: 'first', nickname: '同名' }], next_after_user_id: firstID });
    if (url.includes('/members?after_user_id=' + firstID)) return reply({ members: [{ user_id: secondID, username: 'second', nickname: '<img>真实成员' }], next_after_user_id: '0' });
    if (url.endsWith('/0/assignee')) {
      assert.deepEqual(JSON.parse(options.body), { assignee_id: secondID, expected_revision: '1' });
      item.draft.assignee_id = secondID; item.draft.assignee_resolution = 'selected'; item.draft.revision = '2';
      return reply(itemData(plain(item)));
    }
    return reply(collection([item, draft(1)]));
  });
  await load(fields);
  const card = controls(fields, 0);
  assert.equal(calls.length, 1);
  await fields.btnMultiMembers.onclick();
  assert.equal(fields.btnMultiMoreMembers.disabled, false);
  assert.match(fields.multiDraftSummary.textContent, /more pages available/);
  assert.equal(card.assigneeID.children.some(option => option.value === secondID), false);
  await fields.btnMultiMoreMembers.onclick();
  assert.equal(fields.btnMultiMoreMembers.disabled, true);
  const choice = card.assigneeID.children.find(option => option.value === secondID);
  assert.equal(choice.textContent, '<img>真实成员 #' + secondID);
  edit(card.assigneeID, secondID);
  assert.equal(card.confirm.disabled, true);
  assert.equal(card.saveAssignee.disabled, false);
  await card.saveAssignee.onclick();
  assert.equal(context.multiDraftPage.snapshot(0).draft.assignee_id, secondID);
  assert.match(text(card.card), /Original assignee name: 同名/);
  assert.equal(card.confirm.disabled, false);
  assert.equal(visit(card.card).some(node => node.tagName === 'IMG'), false);
});

test('failed writes require reload, preserve local inputs and never change another item', async () => {
  const items = [draft(0), draft(1)], calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push(url);
    if (options.method === 'PUT') return reply(null, 409);
    if (url.endsWith('/drafts/0')) {
      const fresh = plain(items[0]); fresh.draft.title = 'Other saved title'; fresh.draft.revision = '2';
      return reply(itemData(fresh));
    }
    return reply(collection(items));
  });
  await load(fields);
  const card = controls(fields, 0);
  edit(card.title, 'My retained title');
  edit(card.dueInput, '2026-10-06T18:00');
  await card.saveText.onclick();
  assert.equal(context.multiDraftPage.reload.has(0), true);
  assert.equal(card.title.value, 'My retained title');
  assert.equal(card.dueInput.value, '2026-10-06T18:00');
  assert.equal(card.saveText.disabled, true); assert.equal(card.confirm.disabled, true);
  assert.equal(controls(fields, 1).confirm.disabled, false);
  await card.loadItem.onclick();
  assert.equal(context.multiDraftPage.reload.has(0), false);
  assert.equal(card.title.value, 'My retained title');
  assert.equal(card.dueInput.value, '2026-10-06T18:00');
  assert.match(text(card.card), /Saved title: Other saved title/);
  assert.equal(card.confirm.disabled, true);
  assert.equal(calls.length, 3);
});

test('confirmation and skip act on one item only and do not automatically process the next item', async () => {
  const items = [draft(0), draft(1)], calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push(url);
    if (!options.method) return reply(collection(plain(items)));
    if (url.endsWith('/0/confirm')) {
      assert.deepEqual(JSON.parse(options.body), { expected_title: 'Task 0', expected_description: '', expected_revision: '1',
        expected_assignee_id: '0', expected_due_at_unix_ms: 0, expected_deadline_resolution: 'none' });
      items[0].status = 'succeeded'; items[0].task_id = '9007199254740993';
      return reply(itemData(plain(items[0])));
    }
    assert.ok(url.endsWith('/1/skip'));
    items[1].status = 'skipped'; items[1].reply_status = 'disabled';
    return reply(itemData(plain(items[1])));
  });
  await load(fields);
  await controls(fields, 0).confirm.onclick();
  assert.equal(calls.length, 2);
  assert.equal(context.multiDraftPage.snapshot(1).status, 'waiting_confirmation');
  assert.equal(controls(fields, 0).retryReply.disabled, true, 'confirmation does not count as a GET review');
  await controls(fields, 1).skip.onclick();
  assert.equal(calls.length, 3);
  assert.match(fields.multiDraftSummary.textContent, /1 tasks created, 1 skipped/);
});

test('reply retry requires a GET review after confirmation and sends only an empty POST to the target item', async () => {
  const items = [draft(0), draft(1)], calls = [];
  const { fields } = boot(async (url, options) => {
    calls.push([url, options]);
    if (url.endsWith('/0/confirm')) { items[0].status = 'succeeded'; items[0].task_id = '9007199254740993'; return reply(itemData(plain(items[0]))); }
    if (url.endsWith('/0/reply/retry')) {
      assert.equal(options.method, 'POST'); assert.equal(options.body, undefined);
      items[0].reply_status = 'accepted'; items[0].reply_msg_id = 'bot-task:' + runID;
      return reply(itemData(plain(items[0])));
    }
    if (url.endsWith('/drafts/0')) return reply(itemData(plain(items[0])));
    return reply(collection(plain(items)));
  });
  await load(fields);
  const card = controls(fields, 0);
  await card.confirm.onclick();
  assert.equal(card.retryReply.disabled, true);
  await card.retryReply.onclick();
  assert.equal(calls.length, 2);
  await card.loadItem.onclick();
  assert.equal(card.retryReply.disabled, false);
  await card.retryReply.onclick();
  assert.equal(calls.length, 4);
  assert.equal(card.retryReply.disabled, true);
  assert.match(text(card.card), /IM accepted this reply/);
});

test('in-flight save disables every new edit/action and a scope change discards its late result and error', async () => {
  const pending = deferred(), calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push(url);
    return options.method === 'PUT' ? pending.promise : reply(collection());
  });
  await load(fields);
  const card = controls(fields, 0);
  edit(card.title, 'Changed');
  const saving = card.saveText.onclick();
  assert.equal(card.title.disabled, true);
  assert.equal(card.confirm.disabled, true);
  assert.equal(controls(fields, 1).confirm.disabled, true);
  assert.equal(fields.btnMultiLoad.disabled, true);
  fields.toUserId.value = '301'; context.clearTaskDraftResult();
  fields.toUserId.value = '300'; context.clearTaskDraftResult();
  pending.reject(new Error('Bearer test-token private storage'));
  await saving;
  assert.equal(context.multiDraftPage.items.length, 0);
  assert.equal(fields.multiDraftItems.children.length, 0);
  assert.equal(fields.multiDraftMessage.textContent, '');
  assert.equal(fields.multiDraftRunID.value, '');
  assert.equal(calls.length, 2);
});

test('first pending load is invalidated before any saved scope or cards and old single clearing keeps a current collection', async () => {
  const pending = deferred();
  const { context, fields } = boot(async () => pending.promise);
  const loading = load(fields);
  fields.token.value = 'other-token'; context.clearTaskDraftResult();
  pending.resolve(reply(collection()));
  await loading;
  assert.equal(fields.multiDraftItems.children.length, 0);
  assert.equal(context.multiDraftPage.runID, '');
  const second = boot(async () => reply(collection()));
  await load(second.fields);
  second.fields.draftInstruction.value = 'single instruction only';
  second.context.clearTaskDraftResult();
  assert.equal(second.context.multiDraftPage.items.length, 2);
  assert.equal(second.fields.multiDraftItems.children.length, 2);
});

test('manual run input disables stale cards; invalid date, dirty edits and unknown request keys cause no write', async () => {
  let calls = 0;
  const { fields } = boot(async () => { calls++; return reply(collection()); });
  await load(fields);
  const card = controls(fields, 0);
  edit(card.dueInput, '2026-02-30T18:00');
  assert.equal(card.saveDeadline.disabled, true); assert.equal(card.confirm.disabled, true);
  await card.saveDeadline.onclick();
  assert.equal(calls, 1);
  fields.multiDraftRunID.value = '9007199254741013'; fields.multiDraftRunID.oninput();
  assert.equal(card.confirm.disabled, true); assert.equal(card.saveText.disabled, true);
  await card.confirm.onclick(); await card.saveText.onclick();
  assert.equal(calls, 1, 'a retained old card cannot write while another run ID is entered');
  fields.multiDraftRequestKey.value = 'unknown-key'; fields.multiDraftRequestKey.oninput();
  fields.multiDraftInstruction.value = 'new task'; await fields.btnMultiPrepare.onclick();
  assert.equal(calls, 1);
  assert.match(fields.multiDraftMessage.textContent, /unknown request key/);
});

test('generation displays its own reference, stable retry key, and only an explicit New key starts a different intention', async () => {
  const calls = [];
  const { context, fields } = boot(async (url, options) => {
    calls.push([url, options]);
    return options.method === 'POST' ? reply({ run_id: runID }) : reply(collection());
  });
  context.multiDraftPage.now = () => 1791000000123;
  fields.multiDraftInstruction.value = 'extract tasks';
  const key = fields.multiDraftRequestKey.value;
  await fields.btnMultiPrepare.onclick();
  assert.equal(calls.length, 2);
  assert.equal(calls[0][1].headers['Idempotency-Key'], key);
  assert.deepEqual(JSON.parse(calls[0][1].body), { instruction: 'extract tasks', instruction_reference_unix_ms: 1791000000123 });
  assert.match(fields.multiDraftReference.textContent, /Current generation request reference:/);
  assert.match(fields.multiDraftReference.textContent, /Asia\/Shanghai; UTC:/);
  assert.equal(fields.multiDraftRunID.value, runID);
  fields.multiDraftInstruction.value = 'different tasks'; await fields.btnMultiPrepare.onclick();
  assert.equal(calls.length, 2);
  assert.match(fields.multiDraftMessage.textContent, /new key/);
  await fields.btnMultiNewKey.onclick();
  assert.notEqual(fields.multiDraftRequestKey.value, key);
  assert.equal(fields.multiDraftItems.children.length, 0);
  await fields.btnMultiPrepare.onclick();
  assert.equal(calls.length, 4);
});

test('current errors and controller messages redact the login token without rendering markup', async () => {
  const { context, fields } = boot(async () => { throw new Error('Bearer test-token <script>private</script>'); });
  await load(fields);
  assert.doesNotMatch(fields.multiDraftMessage.textContent, /test-token/);
  context.multiDraftPage.message = 'test-token'; context.multiDraftPage._notify();
  assert.doesNotMatch(fields.multiDraftMessage.textContent, /test-token/);
  assert.equal(visit(fields.multiDraftMessage).some(node => node.tagName === 'SCRIPT'), false);
});

test('maximum revision remains readable and confirmable but changed saves cannot advance its version', async () => {
  const item = draft(0);
  item.draft.revision = '9223372036854775807';
  item.draft.assignee_resolution = 'unassigned';
  item.draft.deadline.resolution = 'unset';
  const { fields } = boot(async () => reply(collection([item, draft(1)])));
  await load(fields);
  const card = controls(fields, 0);
  assert.equal(card.saveText.disabled, false, 'unchanged text does not increment the revision');
  assert.equal(card.saveAssignee.disabled, false, 'already unassigned can be saved without increment');
  assert.equal(card.saveDeadline.disabled, false, 'already unset can be saved without increment');
  assert.equal(card.confirm.disabled, false);
  edit(card.title, 'Changed task');
  edit(card.dueInput, '2026-10-07T18:00');
  assert.equal(card.saveText.disabled, true);
  assert.equal(card.saveDeadline.disabled, true);
  assert.equal(card.confirm.disabled, true);
});
