const test = require('node:test');
const assert = require('node:assert/strict');
const { page, draft, collection, itemData, reply, deferred, plain } = require('./multi-draft-test-helper.cjs');
const scripts = ['multi-draft-core.js', 'multi-draft-actions.js', 'multi-draft-view.js'];

function text(node) { return [node.textContent || '', ...node.children.map(text)].join(' '); }

test('complete native page processes three items independently and only retries the original card after reading', async () => {
  const items = [draft(0), draft(1), draft(2)];
  const runID = collection().run_id;
  const calls = [];
  let replyAttempt = 0;
  const { context, fields } = page(async (url, options = {}) => {
    const method = options.method || 'GET', body = options.body ? JSON.parse(options.body) : null;
    calls.push({ url, method, body, token: options.headers.Authorization });
    assert.equal(options.headers.Authorization, 'Bearer test-token');
    if (url.endsWith('/task-draft-collections')) {
      assert.equal(method, 'POST');
      assert.equal(body.instruction, 'Extract the three tasks');
      assert.ok(Number.isSafeInteger(body.instruction_reference_unix_ms) && body.instruction_reference_unix_ms > 0);
      for (const item of items) item.draft.deadline.instruction_reference_unix_ms = body.instruction_reference_unix_ms;
      return reply({ run_id: runID });
    }
    if (url === '/api/v1/agent/runs/' + runID + '/drafts') return reply(plain(collection(items)));
    const match = url.match(/\/drafts\/(\d)(.*)$/);
    assert.ok(match, 'only explicit collection item routes are used');
    const index = Number(match[1]), suffix = match[2], item = items[index];
    if (method === 'GET') return reply(plain(itemData(item, { item_count: 3 })));
    if (method === 'PUT' && suffix === '') {
      assert.equal(body.expected_revision, item.draft.revision);
      item.draft.title = body.title;
      item.draft.description = body.description;
      item.draft.revision = (BigInt(item.draft.revision) + 1n).toString();
    } else if (suffix === '/confirm') {
      assert.equal(body.expected_title, item.draft.title);
      assert.equal(body.expected_description, item.draft.description);
      assert.equal(body.expected_revision, item.draft.revision);
      assert.equal(body.expected_assignee_id, '0');
      assert.equal(body.expected_due_at_unix_ms, 0);
      assert.equal(body.expected_deadline_resolution, 'none');
      item.status = 'succeeded';
      item.task_id = String(9007199254741201n + BigInt(index));
      item.reply_status = index === 0 ? 'pending' : 'accepted';
      item.reply_msg_id = 'bot-task:' + runID + (index ? ':' + index : '');
    } else if (suffix === '/skip') {
      assert.deepEqual(body, { expected_revision: item.draft.revision });
      item.status = 'skipped';
      item.reply_status = 'disabled';
    } else if (suffix === '/reply/retry') {
      assert.equal(options.body, undefined, 'reply retry has no editable body');
      assert.equal(index, 0);
      if (++replyAttempt === 1) throw new Error('response lost');
      item.reply_status = 'accepted';
    } else assert.fail('unexpected write ' + url);
    return reply(plain(itemData(item, { item_count: 3 })));
  }, scripts);
  const controller = context.multiDraftPage;
  await controller.prepare('Extract the three tasks');
  assert.equal(controller.items.length, 3);
  controller.setEdit(0, { title: 'Reviewed task', description: 'Saved detail' });
  controller.setEdit(1, { title: 'Retained second input' });
  await controller.saveText(0);
  assert.equal(controller.edits.get(1).title, 'Retained second input');
  await controller.confirm(0);
  assert.equal(controller.items[0].status, 'succeeded');
  assert.equal(controller.items[0].reply_status, 'pending');
  assert.match(text(fields.multiDraftItems), /9007199254741201/);
  const beforeRetry = calls.length;
  await assert.rejects(controller.retryReply(0));
  assert.equal(calls.length, beforeRetry, 'confirmation response does not authorize an unreviewed retry');
  await controller.loadItem(0);
  await assert.rejects(controller.retryReply(0));
  assert.equal(controller.items[0].task_id, '9007199254741201');
  assert.ok(controller.reload.has(0));
  assert.ok(!controller.reload.has(1));
  await controller.saveText(1);
  await controller.confirm(1);
  await controller.skip(2);
  await controller.loadItem(0);
  await controller.retryReply(0);
  assert.deepEqual(plain(controller.items.map(item => [item.status, item.reply_status])),
    [['succeeded', 'accepted'], ['succeeded', 'accepted'], ['skipped', 'disabled']]);
  assert.equal(calls.filter(call => call.url.endsWith('/confirm')).length, 2);
  assert.equal(calls.filter(call => call.url.endsWith('/reply/retry')).length, 2);
  assert.equal(calls.filter(call => call.url.endsWith('/skip')).length, 1);
  assert.ok(calls.every(call => !/\/runs\/\d+\/draft(?:\/?$|\/)/.test(call.url)), 'no old single endpoint');
});

test('all-skipped collection preserves evidence and refuses to create or reply', async () => {
  const items = [draft(0), draft(1)], calls = [];
  const { context, fields } = page(async (url, options = {}) => {
    calls.push(url);
    if (options.method !== 'POST') return reply(plain(collection(items)));
    assert.ok(url.endsWith('/skip'));
    const index = Number(url.match(/\/drafts\/(\d)/)[1]);
    items[index].status = 'skipped';
    items[index].reply_status = 'disabled';
    return reply(plain(itemData(items[index])));
  }, scripts);
  const controller = context.multiDraftPage;
  await controller.load(collection().run_id);
  await controller.skip(0);
  await controller.skip(1);
  const previous = calls.length;
  await assert.rejects(controller.confirm(0));
  await assert.rejects(controller.retryReply(1));
  assert.equal(calls.length, previous);
  assert.ok(controller.items.every(item => item.task_id === '0' && item.draft.revision === '1'));
  assert.match(text(fields.multiDraftItems), /skipped/i);
  assert.ok(!text(fields.multiDraftItems).includes('9007199254741201'));
});

test('old page context hook discards a late collection response and preserves independent single-panel changes', async () => {
  const pending = deferred();
  const { context, fields } = page(async (_url, options) => {
    if (options.headers.Authorization === 'Bearer test-token') return pending.promise;
    return reply(collection());
  }, scripts);
  const controller = context.multiDraftPage;
  const load = controller.load(collection().run_id);
  fields.token.value = 'new-token';
  context.clearTaskDraftResult();
  pending.resolve(reply(collection()));
  await load.catch(() => {});
  assert.equal(controller.items.length, 0);
  assert.equal(fields.multiDraftItems.children.length, 0);
  await controller.load(collection().run_id);
  assert.equal(controller.scope.token, 'new-token');
  controller.setEdit(0, { title: 'Keep my independent edit' });
  context.clearTaskDraftResult();
  assert.equal(controller.items.length, 2, 'single-panel reset in same scope does not clear collection');
  assert.equal(controller.edits.get(0).title, 'Keep my independent edit');
});
