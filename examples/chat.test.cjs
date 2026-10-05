const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

function page(fetch) {
  const logs = [];
  const logEntries = [];
  let randomCounter = 0;
  const groupSelect = {
    value: '',
    options: [{ value: '', textContent: 'Choose a group' }],
    appendChild(option) { this.options.push(option); },
    replaceChildren(...options) { this.options = options; this.value = ''; }
  };
  const taskList = {
    children: [],
    appendChild(child) { this.children.push(child); },
    replaceChildren(...children) { this.children = children; }
  };
  const fields = {
    wsUrl: { value: '' },
    token: { value: 'test-token' },
    agentTriggerIdentityHint: { textContent: '' },
    teamId: { value: '' },
    newGroupName: { value: '' },
    groupRequestKey: { value: '' },
    teamGroupSelect: groupSelect,
    toUserId: { value: '' },
    chatType: { value: '1' },
    msgContent: { value: '' },
    taskTitle: { value: '' },
    taskDescription: { value: '' },
    taskAssigneeID: { value: '' },
    taskDueAt: { value: '' },
    taskSourceGroupID: { value: '' },
    taskSourceMessageID: { value: '' },
    taskRequestKey: { value: '' },
    aiQuestion: { value: '' },
    aiAnswer: { textContent: '' },
    btnAskAI: { disabled: false },
    draftInstruction: { value: '' },
    draftRequestKey: { value: '' },
    draftRunID: { value: '' },
    draftResult: { textContent: '' },
    draftReferenceSummary: { textContent: '' },
    draftAssigneeSummary: { textContent: '' },
    draftDeadlineSummary: { textContent: '' },
    draftDeadlineEvidence: { textContent: '' },
    draftDueAt: { value: '', disabled: true },
    btnSaveDraftDeadline: { disabled: true },
    btnClearDraftDeadline: { disabled: true },
    draftMemberHint: { textContent: '' },
    draftAssigneeSelect: { value: '', options: [], disabled: true, replaceChildren(...options) { this.options = options; this.value = ''; } },
    btnLoadDraftMembers: { disabled: true },
    btnMoreDraftMembers: { disabled: true },
    btnSaveDraftAssignee: { disabled: true },
    btnPrepareDraft: { disabled: false },
    btnLoadDraft: { disabled: false },
    draftEditTitle: { value: '', disabled: true },
    draftEditDescription: { value: '', disabled: true },
    btnSaveDraft: { disabled: true },
    btnConfirmDraft: { disabled: true, textContent: '' },
    draftConfirmHint: { textContent: '' },
    btnRetryTaskReply: { disabled: true, textContent: '' },
    taskReplyHint: { textContent: '' },
    taskList
  };
  const context = {
    fetch,
    crypto: { getRandomValues(bytes) { bytes.fill(++randomCounter); return bytes; } },
    location: { protocol: 'http:', hostname: 'gateway.test' },
    document: {
      getElementById: id => fields[id],
      createElement: () => ({ value: '', textContent: '', dataset: {}, children: [], appendChild(child) { this.children.push(child); } })
    },
    encodeURIComponent
  };
  context.WebSocket = class {
    static OPEN = 1;
    constructor(url) {
      this.url = url;
      this.readyState = 1;
      this.sent = [];
      context.socket = this;
    }
    send(payload) { this.sent.push(payload); }
  };
  vm.runInNewContext(script + '\nthis.pullOfflineMessages = pullOfflineMessages; this.isNewChatMessage = isNewChatMessage; this.loadTeamGroupHistory = loadTeamGroupHistory; this.loadTeamGroups = loadTeamGroups; this.selectTeamGroup = selectTeamGroup; this.joinTeamGroup = joinTeamGroup; this.createTeamGroup = createTeamGroup; this.newGroupRequestKey = newGroupRequestKey; this.loadTasks = loadTasks; this.createTask = createTask; this.askAI = askAI; this.doAskAI = doAskAI; this.clearAIAnswer = clearAIAnswer; this.prepareTaskDraft = prepareTaskDraft; this.doPrepareTaskDraft = doPrepareTaskDraft; this.loadTaskDraft = loadTaskDraft; this.newDraftRequestKey = newDraftRequestKey;', context);
  context.log = text => {
    logs.push(text);
    const entry = { children: [], appendChild(child) { this.children.push(child); } };
    logEntries.push(entry);
    return entry;
  };
  context.setWsStatus = () => {};
  return { context, logs, logEntries, fields };
}

function reply(data) {
  return { ok: true, status: 200, json: async () => data };
}

test('live, history and offline messages distinguish bot and user with the same ID', async () => {
  for (const source of ['live', 'history', 'offline']) {
    const bot = { id: '9', msg_id: 'bot', from_id: '42', sender_type: 2,
      initiator_id: '9007199254740995', to_id: '300', chat_type: 2, content: 'created task' };
    const user = { id: '8', msg_id: 'user', from_id: '42', content: 'ordinary message' };
    const { context, fields, logs } = page(async url => {
      if (url.endsWith('/offline/ack')) return reply({ code: 0 });
      return reply({ code: 0, data: source === 'offline' ? [bot, user] :
        { messages: [bot, user], next_before_message_id: '0' } });
    });
    fields.teamId.value = '200';
    fields.toUserId.value = '300';
    fields.chatType.value = '2';
    if (source === 'live') {
      context.doConnect();
      for (const data of [bot, user]) context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data }) });
    } else if (source === 'history') await context.loadTeamGroupHistory();
    else await context.pullOfflineMessages('test-token');
    assert.equal(logs.filter(line => line.includes('AI assistant #42 (confirmed by user #9007199254740995)')).length, 1, source);
    assert.equal(logs.filter(line => line.includes('User #42')).length, 1, source);
  }
});

test('bot repeated across live, history and offline appears once and is acknowledged', async () => {
  const bot = { id: '9', msg_id: 'same-result', from_id: '42', sender_type: 2,
    initiator_id: '9007199254740995', to_id: '300', chat_type: 2, content: 'created task' };
  let ackIDs;
  const { context, fields, logs } = page(async (url, options) => {
    if (url.endsWith('/offline/ack')) {
      ackIDs = JSON.parse(options.body).message_ids;
      return reply({ code: 0 });
    }
    return reply({ code: 0, data: url.endsWith('/offline') ? [bot] :
      { messages: [bot], next_before_message_id: '0' } });
  });
  fields.teamId.value = '200';
  fields.toUserId.value = '300';
  fields.chatType.value = '2';
  context.doConnect();
  context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data: bot }) });
  await context.loadTeamGroupHistory();
  await context.pullOfflineMessages('test-token');
  assert.equal(logs.filter(line => line.includes('AI assistant #42 (confirmed by user #9007199254740995)')).length, 1);
  assert.deepEqual(ackIDs, ['9']);
});

test('invalid or unknown bot metadata does not claim a valid AI identity', () => {
  const { context } = page(async () => reply({ code: 0 }));
  for (const message of [
    { sender_type: 3, from_id: '42', initiator_id: '43' },
    { sender_type: 2, from_id: '42', initiator_id: 9007199254740992 },
    { sender_type: 2, from_id: '42', initiator_id: '0' },
    { sender_type: 2, from_id: '<script>', initiator_id: '43' }
  ]) assert.equal(context.chatSenderLabel(message), 'Unknown sender');
  assert.equal(context.chatSenderLabel({ from_id: '42', sender_type: 0 }), 'User #42');
});

test('task source preview identifies the bot and authorizing user as plain text', async () => {
  const { context } = page(async () => reply({ code: 0, data: { messages: [
    { id: '9', from_id: '42', sender_type: 2, initiator_id: '9007199254740995', content: '<script>task</script>' }
  ] } }));
  const preview = { textContent: '' };
  await context.loadTaskSource({ source_group_id: '300', source_message_id: '9' }, '200', preview);
  assert.equal(preview.textContent, 'AI assistant #42 (confirmed by user #9007199254740995): <script>task</script>');
});

function taskCardMessage(overrides = {}) {
  return { id: '9', msg_id: 'task-result', from_id: '42', sender_type: 2,
    initiator_id: '9007199254740995', to_id: '300', chat_type: 2, content_type: 4,
    content: JSON.stringify({ version: 1, task_id: '9223372036854775807', title: '<img src=x onerror=alert(1)>' }),
    ...overrides };
}

function renderedChatCards(logEntries) {
  return logEntries.flatMap(entry => entry.children.filter(child => child.className === 'task-card'));
}

test('live, history and offline render identical safe task creation cards without further actions', async () => {
  for (const source of ['live', 'history', 'offline']) {
    const message = taskCardMessage();
    const calls = [];
    const { context, fields, logs, logEntries } = page(async (url, options) => {
      calls.push({ url, options });
      if (url.endsWith('/offline/ack')) return reply({ code: 0 });
      return reply({ code: 0, data: source === 'offline' ? [message] :
        { messages: [message], next_before_message_id: '0' } });
    });
    fields.teamId.value = '200';
    fields.toUserId.value = '300';
    fields.chatType.value = '2';
    if (source === 'live') {
      context.doConnect();
      context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data: message }) });
    } else if (source === 'history') await context.loadTeamGroupHistory();
    else await context.pullOfflineMessages('test-token');
    const cards = renderedChatCards(logEntries);
    assert.equal(cards.length, 1, source);
    assert.deepEqual(Array.from(cards[0].children, child => child.textContent),
      ['Task created', '<img src=x onerror=alert(1)>', 'Task #9223372036854775807']);
    assert.ok(cards[0].children.every(child => child.innerHTML === undefined));
    assert.equal(logs.filter(line => line.includes('Task created:')).length, 1);
    assert.ok(calls.every(call => call.url.includes('/messages?') || call.url.includes('/message/offline')));
    assert.ok(calls.every(call => !call.options.method || call.url.endsWith('/offline/ack')));
  }
});

test('a task card delivered through all three paths renders once and still acknowledges offline duplicates', async () => {
  const message = taskCardMessage();
  let ackIDs;
  const { context, fields, logEntries } = page(async (url, options) => {
    if (url.endsWith('/offline/ack')) {
      ackIDs = JSON.parse(options.body).message_ids;
      return reply({ code: 0 });
    }
    return reply({ code: 0, data: url.endsWith('/offline') ? [message] :
      { messages: [message], next_before_message_id: '0' } });
  });
  fields.teamId.value = '200';
  fields.toUserId.value = '300';
  fields.chatType.value = '2';
  context.doConnect();
  context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data: message }) });
  await context.loadTeamGroupHistory();
  await context.pullOfflineMessages('test-token');
  assert.equal(renderedChatCards(logEntries).length, 1);
  assert.deepEqual(ackIDs, ['9']);
});

test('unknown or damaged cards fall back to text and do not block offline processing or acknowledgement', async () => {
  const messages = [
    taskCardMessage({ id: '9', msg_id: 'future', content: '{"version":2,"task_id":"9","title":"<script>future</script>"}' }),
    taskCardMessage({ id: '10', msg_id: 'damaged', content: '{broken JSON' }),
    taskCardMessage({ id: '11', msg_id: 'known' })
  ];
  let ackIDs;
  const { context, logs, logEntries } = page(async (url, options) => {
    if (url.endsWith('/offline/ack')) {
      ackIDs = JSON.parse(options.body).message_ids;
      return reply({ code: 0 });
    }
    return reply({ code: 0, data: messages });
  });
  await context.pullOfflineMessages('test-token');
  assert.equal(logs.filter(line => line.includes('Unsupported or invalid task card:')).length, 2);
  assert.equal(renderedChatCards(logEntries).length, 1);
  assert.deepEqual(ackIDs, ['9', '10', '11']);
});

test('unsafe IDs, forged card identities and invalid card fields never render a task result', () => {
  const { context, logEntries } = page(async () => reply({ code: 0 }));
  const card = { version: 1, task_id: '9', title: 'title' };
  const invalid = [
    taskCardMessage({ sender_type: 1 }), taskCardMessage({ chat_type: 1 }),
    taskCardMessage({ initiator_id: 9007199254740992 }), taskCardMessage({ from_id: '0' }),
    taskCardMessage({ to_id: '0' }),
    ...[
      { ...card, task_id: 9007199254740992 }, { ...card, task_id: '9223372036854775808' },
      { ...card, task_id: '09' }, { ...card, task_id: '0' }, { ...card, version: '1' },
      { ...card, title: ' title ' }, { ...card, title: '' }, { ...card, title: '\ud800' },
      { ...card, title: '中'.repeat(201) }, { ...card, url: 'javascript:alert(1)' }
    ].map(value => taskCardMessage({ content: JSON.stringify(value) }))
  ];
  for (const message of invalid) context.renderChatMessage('message: ', message);
  assert.equal(renderedChatCards(logEntries).length, 0);
});

test('task card title limits count Unicode characters and plain text JSON is not promoted to a card', () => {
  const { context, logEntries } = page(async () => reply({ code: 0 }));
  for (const title of ['😀'.repeat(200), '中'.repeat(200)]) {
    context.renderChatMessage('message: ', taskCardMessage({ content: JSON.stringify({ version: 1, task_id: '9', title }) }));
  }
  context.renderChatMessage('message: ', taskCardMessage({ content_type: 1 }));
  assert.equal(renderedChatCards(logEntries).length, 2);
});

test('task source preview uses a readable immutable card result', async () => {
  const { context } = page(async () => reply({ code: 0, data: { messages: [taskCardMessage()] } }));
  const preview = { textContent: '' };
  await context.loadTaskSource({ source_group_id: '300', source_message_id: '9' }, '200', preview);
  assert.equal(preview.textContent,
    'AI assistant #42 (confirmed by user #9007199254740995): Task created: <img src=x onerror=alert(1)> (Task #9223372036854775807)');
});

test('task list uses string ID cursor and renders text safely', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply({ code: 0, data: {
      tasks: [{ task_id: '9007199254740993', title: '<img src=x>', description: 'Check', assignee_id: '0', status: 1, source_group_id: '0', source_message_id: '0', due_at_unix_ms: 0 }],
      next_after_task_id: '9007199254740993'
    } });
    return reply({ code: 0, data: { tasks: [], next_after_task_id: '0' } });
  });
  fields.teamId.value = '9007199254740995';
  await context.loadTasks(true);
  await context.loadTasks(false);
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740995/tasks?limit=20');
  assert.equal(calls[1].url, '/api/v1/teams/9007199254740995/tasks?limit=20&after_task_id=9007199254740993');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(fields.taskList.children[0].children[0].textContent, '<img src=x> (#9007199254740993)');
  assert.equal(fields.taskList.children[0].children[2].textContent, 'In progress · Assignee: unassigned · Due: none');
});

test('task list shows a UTC deadline in the browser local time', async () => {
  const previousTZ = process.env.TZ;
  process.env.TZ = 'Asia/Shanghai';
  try {
    const { context, fields } = page(async () => reply({ code: 0, data: {
      tasks: [{ task_id: '123', title: 'Review', description: '', assignee_id: '0', status: 0,
        source_group_id: '0', source_message_id: '0', due_at_unix_ms: Date.UTC(2026, 9, 1, 10, 30) }],
      next_after_task_id: '0'
    } }));
    fields.teamId.value = '200';
    await context.loadTasks(true);
    assert.equal(fields.taskList.children[0].children[2].textContent,
      'To do · Assignee: unassigned · Due: 2026-10-01 18:30 (local)');
  } finally {
    if (previousTZ === undefined) delete process.env.TZ;
    else process.env.TZ = previousTZ;
  }
});

test('task list skips an invalid deadline instead of displaying a false date', async () => {
  const { context, fields, logs } = page(async () => reply({ code: 0, data: {
    tasks: [{ task_id: '123', title: 'Review', description: '', assignee_id: '0', status: 0,
      source_group_id: '0', source_message_id: '0', due_at_unix_ms: '179084...' }],
    next_after_task_id: '0'
  } }));
  fields.teamId.value = '200';
  await context.loadTasks(true);
  assert.equal(fields.taskList.children.length, 0);
  assert.ok(logs.includes('Skipped invalid task'));
});

test('failed task page can be retried without advancing cursor', async () => {
  const calls = [];
  const { context, fields } = page(async url => {
    calls.push(url);
    if (calls.length === 1) return { ok: false, status: 503 };
    return reply({ code: 0, data: { tasks: [], next_after_task_id: '0' } });
  });
  fields.teamId.value = '200';
  await assert.rejects(context.loadTasks(true), /task list HTTP 503/);
  await context.loadTasks(false);
  assert.equal(calls[0], calls[1]);
});

test('task creation sends exact source IDs and refreshes the list', async () => {
  const calls = [];
  const { context, fields, logs } = page(async (url, options) => {
    calls.push({ url, options });
    if (options.method === 'POST') return reply({ code: 0, data: { task_id: '9007199254740997' } });
    return reply({ code: 0, data: { tasks: [], next_after_task_id: '0' } });
  });
  fields.teamId.value = '9007199254740993';
  fields.taskTitle.value = ' Fix cache ';
  fields.taskAssigneeID.value = '9007199254740995';
  fields.taskSourceGroupID.value = '9007199254740999';
  fields.taskSourceMessageID.value = '9007199254741001';
  const id = await context.createTask();
  assert.equal(id, '9007199254740997');
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740993/tasks');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.match(calls[0].options.headers['Idempotency-Key'], /^task-[a-f0-9]{32}$/);
  assert.deepEqual(JSON.parse(calls[0].options.body), {
    title: 'Fix cache', description: '', assignee_id: '9007199254740995',
    source_group_id: '9007199254740999', source_message_id: '9007199254741001'
  });
  assert.equal(calls[1].url, '/api/v1/teams/9007199254740993/tasks?limit=20');
  assert.equal(fields.taskRequestKey.value, '');
  assert.ok(logs.some(line => line.includes('Created task 9007199254740997')));
});

test('task creation converts local due time to UTC and keeps it for retry', async () => {
  const previousTZ = process.env.TZ;
  process.env.TZ = 'Asia/Shanghai';
  try {
    const calls = [];
    const { context, fields } = page(async (url, options) => {
      if (options.method === 'POST') {
        calls.push(options);
        if (calls.length === 1) return { ok: false, status: 503 };
        return reply({ code: 0, data: { task_id: '123' } });
      }
      return reply({ code: 0, data: { tasks: [], next_after_task_id: '0' } });
    });
    fields.teamId.value = '200';
    fields.taskTitle.value = 'Task';
    fields.taskDueAt.value = '2026-10-01T18:30';
    await assert.rejects(context.createTask(), /task create HTTP 503/);
    const firstBody = JSON.parse(calls[0].body);
    assert.equal(firstBody.due_at_unix_ms, Date.UTC(2026, 9, 1, 10, 30));
    assert.equal(fields.taskDueAt.value, '2026-10-01T18:30');
    await context.createTask();
    assert.equal(calls[0].headers['Idempotency-Key'], calls[1].headers['Idempotency-Key']);
    assert.deepEqual(JSON.parse(calls[1].body), firstBody);
    assert.equal(fields.taskDueAt.value, '');
  } finally {
    if (previousTZ === undefined) delete process.env.TZ;
    else process.env.TZ = previousTZ;
  }
});

test('invalid local due time is rejected before creating a request key', async () => {
  const { context, fields } = page(async () => { throw new Error('invalid due time reached Gateway'); });
  fields.teamId.value = '200';
  fields.taskTitle.value = 'Task';
  fields.taskDueAt.value = '2026-02-30T18:30';
  await assert.rejects(context.createTask(), /invalid local due time/);
  assert.equal(fields.taskRequestKey.value, '');
});

test('uncertain task creation keeps the request key for a retry', async () => {
  const keys = [];
  const { context, fields } = page(async (url, options) => {
    keys.push(options.headers['Idempotency-Key']);
    if (keys.length === 1) return { ok: false, status: 503 };
    return reply({ code: 0, data: { task_id: '123' } });
  });
  fields.teamId.value = '200';
  fields.taskTitle.value = 'Task';
  await assert.rejects(context.createTask(), /task create HTTP 503/);
  assert.match(fields.taskRequestKey.value, /^task-/);
  await context.createTask();
  assert.equal(keys[0], keys[1]);
});

test('task card updates status only after the server accepts it', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (options.method === 'PUT') return reply({ code: 0 });
    return reply({ code: 0, data: { tasks: [
      { task_id: '9007199254740993', title: 'Review', description: '', assignee_id: '0', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: Date.UTC(2026, 9, 1, 10, 30) }
    ], next_after_task_id: '0' } });
  });
  fields.teamId.value = '200';
  await context.loadTasks(true);
  const card = fields.taskList.children[0];
  const oldMeta = card.children[2].textContent;
  card.children[3].value = '2';
  await card.children[4].onclick();
  assert.equal(calls[1].url, '/api/v1/teams/200/tasks/9007199254740993/status');
  assert.equal(calls[1].options.method, 'PUT');
  assert.deepEqual(JSON.parse(calls[1].options.body), { status: 2 });
  assert.equal(card.children[2].textContent, oldMeta.replace('To do', 'Done'));
});

test('denied status update restores the displayed status', async () => {
  const { context, fields, logs } = page(async (url, options) => {
    if (options.method === 'PUT') return { ok: false, status: 403 };
    return reply({ code: 0, data: { tasks: [
      { task_id: '123', title: 'Review', description: '', assignee_id: '0', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: 0 }
    ], next_after_task_id: '0' } });
  });
  fields.teamId.value = '200';
  await context.loadTasks(true);
  const card = fields.taskList.children[0];
  card.children[3].value = '2';
  await card.children[4].onclick();
  assert.equal(card.children[3].value, '0');
  assert.equal(card.children[2].textContent, 'To do · Assignee: unassigned · Due: none');
  assert.ok(logs.some(line => line.includes('Task status failed: task status HTTP 403')));
});

test('source button reads exactly the referenced message through IM history', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply({ code: 0, data: { tasks: [
      { task_id: '123', title: 'Fix', description: '', assignee_id: '0', status: 0,
        source_group_id: '300', source_message_id: '9007199254740993', due_at_unix_ms: 0 }
    ], next_after_task_id: '0' } });
    return reply({ code: 0, data: { messages: [
      { id: '9007199254740993', from_id: '42', content: '<script>alert(1)</script>' }
    ], next_before_message_id: '0' } });
  });
  fields.teamId.value = '200';
  await context.loadTasks(true);
  const card = fields.taskList.children[0];
  await card.children[7].onclick();
  assert.equal(calls[1].url, '/api/v1/teams/200/groups/300/messages?limit=1&before_message_id=9007199254740994');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer test-token');
  assert.equal(card.children[6].textContent, 'User #42: <script>alert(1)</script>');
});

test('source button does not show content when IM denies access', async () => {
  const { context, fields } = page(async (url, options) => {
    if (url.includes('/messages')) return { ok: false, status: 403 };
    return reply({ code: 0, data: { tasks: [
      { task_id: '123', title: 'Fix', description: '', assignee_id: '0', status: 0,
        source_group_id: '300', source_message_id: '400', due_at_unix_ms: 0 }
    ], next_after_task_id: '0' } });
  });
  fields.teamId.value = '200';
  await context.loadTasks(true);
  const card = fields.taskList.children[0];
  await card.children[7].onclick();
  assert.equal(card.children[6].textContent, 'Source unavailable: source history HTTP 403');
});

test('reconnect merges offline and live messages, then confirms string IDs', async () => {
  const calls = [];
  const { context, logs } = page(async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/offline')) {
      return reply({ code: 0, data: [
        { id: '9007199254740993', msg_id: 'already-live' },
        { id: '9007199254740994', msg_id: 'offline-only' }
      ] });
    }
    return reply({ code: 0 });
  });
  context.isNewChatMessage({ msg_id: 'already-live' });
  context.doConnect();
  await context.socket.onopen();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/v1/message/offline');
  assert.equal(calls[1].url, '/api/v1/message/offline/ack');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(calls[1].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[1].options.body).message_ids,
    ['9007199254740993', '9007199254740994']);
  assert.equal(logs.filter(text => text.includes('Offline message:')).length, 1);
});

test('login uses the same-origin Gateway path', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    return reply({ code: 0, data: url.endsWith('/login') ? { token: 'new-token' } : { id: '42' } });
  });
  fields.username = { value: 'alice' };
  fields.password = { value: 'secret' };
  fields.loginStatus = { textContent: '', style: {} };
  await context.doLogin();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/v1/user/login');
  assert.equal(calls[1].url, '/api/v1/user/info');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer new-token');
  assert.equal(fields.token.value, 'new-token');
});

test('failed acknowledgement leaves a retried message hidden but confirms it again', async () => {
  let ackCalls = 0;
  const { context, logs } = page(async url => {
    if (url.endsWith('/offline')) return reply({ code: 0, data: [{ id: '7', msg_id: 'm7' }] });
    ackCalls++;
    return reply({ code: ackCalls === 1 ? 10005 : 0, msg: 'temporary failure' });
  });
  await assert.rejects(context.pullOfflineMessages('test-token'), /offline ack failed/);
  await context.pullOfflineMessages('test-token');
  assert.equal(ackCalls, 2);
  assert.equal(logs.filter(text => text.includes('Offline message:')).length, 1);
});

test('acknowledgement is split into batches of at most 1000 IDs', async () => {
  const batches = [];
  const { context } = page(async (url, options) => {
    if (url.endsWith('/offline')) {
      return reply({ code: 0, data: Array.from({ length: 1001 }, (_, i) => ({
        id: String(i + 1), msg_id: 'm' + (i + 1)
      })) });
    }
    batches.push(JSON.parse(options.body).message_ids);
    return reply({ code: 0 });
  });
  await context.pullOfflineMessages('test-token');
  assert.deepEqual(batches.map(batch => batch.length), [1000, 1]);
});

test('send keeps a large group ID as an exact decimal string', () => {
  const { context, fields } = page(async () => reply({ code: 0 }));
  context.doConnect();
  fields.toUserId.value = '9007199254740993';
  fields.chatType.value = '2';
  fields.msgContent.value = 'hello';
  context.doSend();
  const sent = JSON.parse(context.socket.sent[0]);
  assert.equal(sent.data.to_id, '9007199254740993');
  assert.equal(sent.data.chat_type, 2);
});

test('persisted live message in the selected team group can fill task source', async () => {
  let calls = 0;
  const { context, fields, logs, logEntries } = page(async () => { calls++; return reply({ code: 0 }); });
  fields.teamId.value = '200';
  fields.toUserId.value = '300';
  fields.chatType.value = '2';
  context.doConnect();
  context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data: {
    id: '9007199254740993', msg_id: 'live-team-one', to_id: '300', chat_type: 2, content: 'Fix cache'
  } }) });
  const index = logs.findIndex(line => line.includes('New message from'));
  assert.equal(logEntries[index].children[0].textContent, 'Use for task');
  await logEntries[index].children[0].onclick();
  assert.equal(fields.taskSourceGroupID.value, '300');
  assert.equal(fields.taskSourceMessageID.value, '9007199254740993');
  assert.equal(calls, 0);
});

test('private, other-group and unpersisted live messages have no task source button', () => {
  const { context, fields, logs, logEntries } = page(async () => reply({ code: 0 }));
  fields.teamId.value = '200';
  fields.toUserId.value = '300';
  fields.chatType.value = '2';
  context.doConnect();
  for (const data of [
    { id: '400', msg_id: 'private', to_id: '300', chat_type: 1 },
    { id: '401', msg_id: 'other-group', to_id: '301', chat_type: 2 },
    { msg_id: 'not-persisted', to_id: '300', chat_type: 2 }
  ]) context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data }) });
  const indexes = logs.map((line, index) => line.includes('New message from') ? index : -1).filter(index => index >= 0);
  assert.equal(indexes.length, 3);
  assert.ok(indexes.every(index => logEntries[index].children.length === 0));
});

test('WebSocket defaults to the page host and keeps a manual override', () => {
  const { context, fields } = page(async () => reply({ code: 0 }));
  context.doConnect();
  assert.equal(context.socket.url, 'ws://gateway.test:8081/ws?token=test-token');
  fields.wsUrl.value = 'wss://custom.test/chat/';
  context.doConnect();
  assert.equal(context.socket.url, 'wss://custom.test/chat/ws?token=test-token');
});

test('send rejects an out-of-range group ID before WebSocket transmission', () => {
  const { context, fields } = page(async () => reply({ code: 0 }));
  context.doConnect();
  fields.toUserId.value = '9223372036854775808';
  fields.msgContent.value = 'hello';
  context.doSend();
  assert.equal(context.socket.sent.length, 0);
});

test('group history uses exact string cursor and merges with live and offline messages', async () => {
  const calls = [];
  const { context, fields, logs } = page(async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/offline')) {
      return reply({ code: 0, data: [{ id: '50', msg_id: 'offline' }] });
    }
    if (url.endsWith('/offline/ack')) return reply({ code: 0 });
    if (url.includes('before_message_id=')) {
      return reply({ code: 0, data: { messages: [
        { id: '9007199254740992', msg_id: 'history-one' },
        { id: '9007199254740991', msg_id: 'history-two' }
      ], next_before_message_id: '0' } });
    }
    return reply({ code: 0, data: { messages: [
      { id: '9007199254740995', msg_id: 'live' },
      { id: '9007199254740994', msg_id: 'offline' },
      { id: '9007199254740993', msg_id: 'history-one' }
    ], next_before_message_id: '9007199254740993' } });
  });
  context.isNewChatMessage({ msg_id: 'live' });
  await context.pullOfflineMessages('test-token');
  fields.teamId.value = '9007199254740997';
  fields.toUserId.value = '9007199254740999';
  fields.chatType.value = '2';
  await context.loadTeamGroupHistory();
  await context.loadTeamGroupHistory();
  await context.loadTeamGroupHistory();
  const historyCalls = calls.filter(call => call.url.includes('/messages'));
  assert.equal(historyCalls.length, 2);
  assert.equal(historyCalls[0].url, '/api/v1/teams/9007199254740997/groups/9007199254740999/messages?limit=20');
  assert.equal(historyCalls[1].url, historyCalls[0].url + '&before_message_id=9007199254740993');
  assert.equal(historyCalls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(logs.filter(text => text.includes('History message:')).length, 2);
});

test('history message button fills task source without creating a task', async () => {
  const calls = [];
  const { context, fields, logs, logEntries } = page(async (url, options) => {
    calls.push({ url, options });
    return reply({ code: 0, data: {
      messages: [{ id: '9007199254740993', msg_id: 'source-one', content: 'Fix cache' }],
      next_before_message_id: '0'
    } });
  });
  fields.teamId.value = '9007199254740995';
  fields.toUserId.value = '9007199254740997';
  fields.chatType.value = '2';
  await context.loadTeamGroupHistory();
  assert.equal(logEntries.length, 1);
  assert.equal(logEntries[0].children[0].textContent, 'Use for task');
  await logEntries[0].children[0].onclick();
  assert.equal(fields.taskSourceGroupID.value, '9007199254740997');
  assert.equal(fields.taskSourceMessageID.value, '9007199254740993');
  assert.equal(calls.length, 1);
  assert.ok(logs.some(line => line.includes('enter a title before creating')));
});

test('old history button cannot fill task source after switching teams', async () => {
  const { context, fields, logs, logEntries } = page(async () => reply({ code: 0, data: {
    messages: [{ id: '400', msg_id: 'source-two' }], next_before_message_id: '0'
  } }));
  fields.teamId.value = '200';
  fields.toUserId.value = '300';
  fields.chatType.value = '2';
  await context.loadTeamGroupHistory();
  fields.teamId.value = '201';
  await logEntries[0].children[0].onclick();
  assert.equal(fields.taskSourceGroupID.value, '');
  assert.equal(fields.taskSourceMessageID.value, '');
  assert.ok(logs.some(line => line.includes('Cannot select source: team changed')));
});

test('failed history page keeps the same cursor for retry', async () => {
  const calls = [];
  let fail = true;
  const { context, fields } = page(async url => {
    calls.push(url);
    if (url.includes('before_message_id=7') && fail) {
      fail = false;
      return { ok: false, status: 503 };
    }
    return reply({ code: 0, data: {
      messages: url.includes('before_message_id=7') ? [] : [{ id: '7', msg_id: 'first' }],
      next_before_message_id: url.includes('before_message_id=7') ? '0' : '7'
    } });
  });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  await context.loadTeamGroupHistory();
  await assert.rejects(context.loadTeamGroupHistory(), /history HTTP 503/);
  await context.loadTeamGroupHistory();
  assert.equal(calls[1], calls[2]);
});

test('switching groups starts history at the first page again', async () => {
  const calls = [];
  const { context, fields } = page(async url => {
    calls.push(url);
    return reply({ code: 0, data: { messages: [{ id: '8', msg_id: 'group-' + calls.length }], next_before_message_id: '8' } });
  });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  await context.loadTeamGroupHistory();
  fields.toUserId.value = '4';
  await context.loadTeamGroupHistory();
  assert.equal(calls.length, 2);
  assert.match(calls[1], /\/groups\/4\/messages\?limit=20$/);
});

test('team directory pages keep large IDs exact and selecting a group prepares group chat', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply({ code: 0, data: {
      groups: [{ group_id: '9007199254740993', name: '<script>alert(1)</script>' }],
      next_after_group_id: '9007199254740993'
    } });
    return reply({ code: 0, data: {
      groups: [{ group_id: '9007199254740995', name: 'Design' }],
      next_after_group_id: '0'
    } });
  });
  fields.teamId.value = '9007199254740997';
  await context.loadTeamGroups();
  await context.loadTeamGroups();
  await context.loadTeamGroups();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740997/groups?limit=20');
  assert.equal(calls[1].url, calls[0].url + '&after_group_id=9007199254740993');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(fields.teamGroupSelect.options[1].value, '9007199254740993');
  assert.equal(fields.teamGroupSelect.options[1].textContent, '<script>alert(1)</script> (#9007199254740993)');
  fields.teamGroupSelect.value = '9007199254740995';
  context.selectTeamGroup();
  assert.equal(fields.toUserId.value, '9007199254740995');
  assert.equal(fields.chatType.value, '2');
});

test('failed directory page retries the same cursor and changing team resets options', async () => {
  const calls = [];
  let fail = true;
  const { context, fields } = page(async url => {
    calls.push(url);
    if (url.includes('after_group_id=7') && fail) {
      fail = false;
      return { ok: false, status: 503 };
    }
    return reply({ code: 0, data: {
      groups: [{ group_id: url.includes('/teams/3/') ? '8' : '7', name: 'Group' }],
      next_after_group_id: url.includes('/teams/3/') ? '0' : '7'
    } });
  });
  fields.teamId.value = '2';
  await context.loadTeamGroups();
  await assert.rejects(context.loadTeamGroups(), /group list HTTP 503/);
  await context.loadTeamGroups();
  assert.equal(calls[1], calls[2]);
  fields.teamId.value = '3';
  await context.loadTeamGroups();
  assert.equal(calls[3], '/api/v1/teams/3/groups?limit=20');
  assert.deepEqual(fields.teamGroupSelect.options.map(option => option.value), ['', '8']);
});

test('self-join posts exact IDs with token and reports server rejection', async () => {
  const calls = [];
  const { context, fields, logs } = page(async (url, options) => {
    calls.push({ url, options });
    return calls.length === 1 ? reply({ code: 0 }) : { ok: false, status: 403 };
  });
  fields.teamId.value = '9007199254740997';
  fields.toUserId.value = '9007199254740993';
  await context.joinTeamGroup();
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740997/groups/9007199254740993/join');
  assert.equal(calls[0].options.method, 'POST');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(fields.chatType.value, '2');
  await assert.rejects(context.joinTeamGroup(), /group join HTTP 403/);
  assert.equal(logs.filter(text => text.includes('Joined team group')).length, 1);
  fields.toUserId.value = '9223372036854775808';
  await assert.rejects(context.joinTeamGroup(), /enter a token, team ID and group ID/);
  assert.equal(calls.length, 2);
});

test('group creation reuses its request key after an uncertain failure and selects the returned group', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (options.method !== 'POST') return reply({ code: 0, data: {
      groups: [{ group_id: '9007199254740993', name: 'Planning' }],
      next_after_group_id: '0'
    } });
    return calls.length === 1 ? { ok: false, status: 503 } :
      reply({ code: 0, data: { group_id: '9007199254740993' } });
  });
  fields.teamId.value = '9007199254740997';
  fields.newGroupName.value = '  Planning  ';
  await assert.rejects(context.createTeamGroup(), /group create HTTP 503/);
  const key = fields.groupRequestKey.value;
  assert.match(key, /^group-[a-f0-9]{32}$/);
  await context.createTeamGroup();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740997/groups');
  assert.equal(calls[0].options.method, 'POST');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(calls[0].options.headers['Idempotency-Key'], key);
  assert.equal(calls[1].options.headers['Idempotency-Key'], key);
  assert.equal(calls[0].options.headers['Content-Type'], 'application/json');
  assert.equal(JSON.parse(calls[0].options.body).name, 'Planning');
  assert.equal(fields.toUserId.value, '9007199254740993');
  assert.equal(fields.chatType.value, '2');
  assert.equal(fields.teamGroupSelect.value, '9007199254740993');
  await context.loadTeamGroups();
  assert.equal(fields.teamGroupSelect.options.filter(option => option.value === '9007199254740993').length, 1);
  assert.equal(fields.groupRequestKey.value, key);
  assert.notEqual(context.newGroupRequestKey(), key);
});

test('group creation rejects invalid input and keeps the key when owner authorization fails', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    return { ok: false, status: 403 };
  });
  fields.teamId.value = '2';
  fields.newGroupName.value = '   ';
  await assert.rejects(context.createTeamGroup(), /group name/);
  assert.equal(calls.length, 0);
  fields.newGroupName.value = 'Planning';
  await assert.rejects(context.createTeamGroup(), /group create HTTP 403/);
  assert.equal(calls.length, 1);
  assert.match(fields.groupRequestKey.value, /^group-[a-f0-9]{32}$/);
  assert.equal(fields.toUserId.value, '');
  fields.groupRequestKey.value = 'invalid key';
  await assert.rejects(context.createTeamGroup(), /invalid request key/);
  assert.equal(calls.length, 1);
});

test('AI ask uses exact team/group IDs and only displays the answer locally', async () => {
  const calls = [];
  const { context, fields, logs } = page(async (url, options) => {
    calls.push({ url, options });
    return reply({ code: 0, data: { answer: '<script>not HTML</script>' } });
  });
  fields.teamId.value = '9007199254740993';
  fields.toUserId.value = '9007199254740995';
  fields.chatType.value = '2';
  fields.aiQuestion.value = '  Summarize tasks  ';
  await context.askAI();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740993/groups/9007199254740995/ask');
  assert.equal(calls[0].options.method, 'POST');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.deepEqual(JSON.parse(calls[0].options.body), { question: 'Summarize tasks' });
  assert.equal(fields.aiAnswer.textContent, '<script>not HTML</script>');
  assert.equal(fields.btnAskAI.disabled, false);
  assert.equal(logs.length, 0);
});

test('AI ask rejects missing identity, invalid IDs, private chat and empty question before fetch', async () => {
  let calls = 0;
  const { context, fields } = page(async () => { calls++; return reply({ code: 0 }); });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.aiQuestion.value = 'Summarize';
  fields.token.value = '';
  await assert.rejects(context.askAI(), /enter a token/);
  fields.token.value = 'test-token';
  fields.teamId.value = '9223372036854775808';
  await assert.rejects(context.askAI(), /team group/);
  fields.teamId.value = '2';
  fields.toUserId.value = 'bad';
  await assert.rejects(context.askAI(), /team group/);
  fields.toUserId.value = '3';
  fields.chatType.value = '1';
  await assert.rejects(context.askAI(), /team group/);
  fields.chatType.value = '2';
  fields.aiQuestion.value = '  ';
  await assert.rejects(context.askAI(), /question/);
  assert.equal(calls, 0);
});

test('AI ask shows a failure and keeps the question for retry', async () => {
  let calls = 0;
  const { context, fields } = page(async () => {
    calls++;
    return calls === 1 ? { ok: false, status: 503 } : reply({ code: 0, data: { answer: 'Retry succeeded' } });
  });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.aiQuestion.value = 'Which tasks?';
  await context.doAskAI();
  assert.equal(fields.aiAnswer.textContent, 'Ask failed: AI HTTP 503');
  assert.equal(fields.aiQuestion.value, 'Which tasks?');
  assert.equal(fields.btnAskAI.disabled, false);
  await context.askAI();
  assert.equal(calls, 2);
  assert.equal(fields.aiAnswer.textContent, 'Retry succeeded');
});

test('AI ask does not show an old answer after the group changes or send twice', async () => {
  let finish;
  let calls = 0;
  const waiting = new Promise(resolve => { finish = resolve; });
  const { context, fields } = page(async () => { calls++; return waiting; });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.aiQuestion.value = 'Summarize';
  const first = context.askAI();
  assert.equal(fields.btnAskAI.disabled, true);
  await context.askAI();
  assert.equal(calls, 1);
  fields.toUserId.value = '4';
  context.clearAIAnswer();
  finish(reply({ code: 0, data: { answer: 'Old group answer' } }));
  await first;
  assert.equal(fields.aiAnswer.textContent, 'Context changed; ask again.');
  assert.equal(fields.btnAskAI.disabled, false);
});

function referencePage(fetch, clock) {
  const result = page(fetch);
  result.context.Date = class extends Date { static now() { return clock.value; } };
  Object.assign(result.fields.teamId, { value: '2' });
  Object.assign(result.fields.toUserId, { value: '3' });
  result.fields.chatType.value = '2';
  result.fields.draftInstruction.value = 'Extract tomorrow task';
  return result;
}

test('generation reference is frozen at first actual submission and displayed only as current request context', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 15, 59, 59, 987) }, calls = [];
  const { context, fields } = referencePage(async (url, options) => {
    calls.push({ url, options });
    return { ok: false, status: 503 };
  }, clock);
  context.newDraftRequestKey();
  clock.value += 2000; // Explicit new key does not capture the clock before the request is actually submitted.
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  assert.deepEqual(JSON.parse(calls[0].options.body), { instruction: 'Extract tomorrow task', instruction_reference_unix_ms: clock.value });
  assert.match(fields.draftReferenceSummary.textContent, /Current generation request reference \(not retrieved from a saved run\)/);
  assert.match(fields.draftReferenceSummary.textContent, /2026-10-04 00:00:01\.987 Asia\/Shanghai/);
  assert.match(fields.draftReferenceSummary.textContent, /UTC: 2026-10-03T16:00:01\.987Z/);
  assert.match(fields.draftReferenceSummary.textContent, /Check your device clock/);
});

test('failed generation retry across Shanghai midnight sends the exact original key and reference', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 15, 59) }, calls = [];
  const { context } = referencePage(async (url, options) => { calls.push({ url, options }); return { ok: false, status: 504 }; }, clock);
  await assert.rejects(context.prepareTaskDraft(), /HTTP 504/);
  clock.value += 3600000;
  await assert.rejects(context.prepareTaskDraft(), /HTTP 504/);
  assert.equal(calls[0].options.headers['Idempotency-Key'], calls[1].options.headers['Idempotency-Key']);
  assert.equal(calls[0].options.body, calls[1].options.body);
});

test('generation intents include identity, group and instruction; a changed intent creates a fresh key and reference', async () => {
  for (const [field, next] of [['token', 'another-token'], ['teamId', '4'], ['toUserId', '5'], ['draftInstruction', 'Different instruction']]) {
    const clock = { value: Date.UTC(2026, 9, 3, 10) }, calls = [];
    const { context, fields } = referencePage(async (url, options) => { calls.push({ url, options }); return { ok: false, status: 503 }; }, clock);
    await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
    fields[field].value = next;
    context.clearTaskDraftResult();
    clock.value += 1000;
    await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
    assert.notEqual(calls[0].options.headers['Idempotency-Key'], calls[1].options.headers['Idempotency-Key'], field);
    assert.equal(JSON.parse(calls[1].options.body).instruction_reference_unix_ms, clock.value, field);
  }
});

test('restoring a known same-intent old key restores its original reference after another key was submitted', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) }, calls = [];
  const { context, fields } = referencePage(async (url, options) => { calls.push({ url, options }); return { ok: false, status: 503 }; }, clock);
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  const oldKey = fields.draftRequestKey.value;
  clock.value += 1000;
  context.newDraftRequestKey();
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  fields.draftRequestKey.value = oldKey;
  context.clearTaskDraftResult();
  clock.value += 1000;
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  assert.equal(calls[0].options.headers['Idempotency-Key'], calls[2].options.headers['Idempotency-Key']);
  assert.equal(calls[0].options.body, calls[2].options.body);
  assert.notEqual(calls[1].options.body, calls[2].options.body);
});

test('unknown manually entered keys are never sent; original in-page bundles still safely recover', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) }, calls = [];
  const { context, fields } = referencePage(async (url, options) => { calls.push({ url, options }); return { ok: false, status: 503 }; }, clock);
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  const original = fields.draftRequestKey.value;
  fields.draftRequestKey.value = 'manual-unrecoverable-key';
  await context.doPrepareTaskDraft();
  assert.match(fields.draftResult.textContent, /unknown request key.*New key.*Run ID.*refresh/);
  assert.equal(calls.length, 1);
  fields.draftRequestKey.value = original;
  clock.value += 1000;
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  assert.equal(calls[0].options.body, calls[1].options.body);
});

test('a refreshed page does not invent the reference for a previously used key and still permits Run ID reads', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) }, { context, fields } = referencePage(async () => ({ ok: false, status: 503 }), clock);
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  let calls = 0;
  const next = referencePage(async () => { calls++; return reply(draftData()); }, clock);
  next.fields.draftRequestKey.value = fields.draftRequestKey.value;
  await assert.rejects(next.context.prepareTaskDraft(), /unknown request key/);
  assert.equal(calls, 0);
  next.fields.draftRunID.value = '9';
  await next.context.loadTaskDraft();
  assert.match(next.fields.draftReferenceSummary.textContent, /does not retrieve its saved instruction reference/);
  assert.equal(calls, 1);
});

test('invalid device reference clocks reject generation before sending any request', async () => {
  for (const value of [0, -1, 1.5, NaN, Infinity, 253402300800000, '1791000000000']) {
    let calls = 0;
    const clock = { value }, { context } = referencePage(async () => { calls++; return reply({ code: 0 }); }, clock);
    await assert.rejects(context.prepareTaskDraft(), /invalid device clock/);
    assert.equal(calls, 0);
  }
});

test('generation success or error cannot set Run ID or read after changed contexts are restored', async () => {
  for (const field of ['token', 'teamId', 'toUserId', 'draftInstruction', 'draftRequestKey']) {
    for (const fails of [false, true]) {
      const clock = { value: Date.UTC(2026, 9, 3, 10) };
      let finish, calls = 0;
      const pending = new Promise((resolve, reject) => { finish = fails ? reject : resolve; });
      const { context, fields } = referencePage(async () => { calls++; return pending; }, clock);
      const work = context.doPrepareTaskDraft(), before = fields[field].value;
      fields[field].value = 'changed'; context.clearTaskDraftResult(); fields[field].value = before;
      finish(fails ? new Error('stale model error') : reply({ code: 0, data: { run_id: '9' } }));
      await work;
      assert.equal(fields.draftRunID.value, '');
      assert.equal(calls, 1);
      assert.doesNotMatch(fields.draftResult.textContent, /stale model error|Run ID: 9/);
      assert.equal(fields.btnConfirmDraft.disabled, true);
    }
  }
});

test('a new key during generation suppresses the original response and obtains a new reference on next submission', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) }, calls = [];
  let finish;
  const pending = new Promise(resolve => { finish = resolve; });
  const { context, fields } = referencePage(async (url, options) => {
    calls.push({ url, options });
    return calls.length === 1 ? pending : { ok: false, status: 503 };
  }, clock);
  const work = context.prepareTaskDraft();
  clock.value += 1000;
  context.newDraftRequestKey();
  finish(reply({ code: 0, data: { run_id: '9' } }));
  await work;
  assert.equal(fields.draftRunID.value, '');
  await assert.rejects(context.prepareTaskDraft(), /HTTP 503/);
  assert.notEqual(calls[0].options.headers['Idempotency-Key'], calls[1].options.headers['Idempotency-Key']);
  assert.equal(JSON.parse(calls[1].options.body).instruction_reference_unix_ms, clock.value);
});

test('generation follow-up read drops its old draft after a context change even when restored', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) };
  let finish, calls = 0;
  const pending = new Promise(resolve => { finish = resolve; });
  const { context, fields } = referencePage(async () => ++calls === 1 ? reply({ code: 0, data: { run_id: '9' } }) : pending, clock);
  const work = context.prepareTaskDraft();
  await new Promise(resolve => setImmediate(resolve));
  fields.token.value = 'changed'; context.clearTaskDraftResult(); fields.token.value = 'test-token';
  finish(reply(draftData()));
  await work;
  assert.equal(fields.draftEditTitle.value, '');
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.doesNotMatch(fields.draftResult.textContent, /Old title/);
});

test('generation reference remains unchanged when a prepared run follow-up read fails and generation is explicitly retried', async () => {
  const clock = { value: Date.UTC(2026, 9, 3, 10) }, calls = [];
  const { context, fields } = referencePage(async (url, options) => {
    calls.push({ url, options });
    return url.endsWith('/task-drafts') ? reply({ code: 0, data: { run_id: '9' } }) : { ok: false, status: 503 };
  }, clock);
  await context.prepareTaskDraft();
  clock.value += 86400000;
  await context.prepareTaskDraft();
  assert.equal(calls[0].options.body, calls[2].options.body);
  assert.equal(calls[0].options.headers['Idempotency-Key'], calls[2].options.headers['Idempotency-Key']);
  assert.equal(fields.draftRunID.value, '9');
});

test('task draft preparation keeps 64-bit IDs exact and only displays the draft', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply({ code: 0, data: { run_id: '9007199254740997' } });
    return reply({ code: 0, data: {
      run_id: '9007199254740997', team_id: '9007199254740993', group_id: '9007199254740995',
      status: 'waiting_confirmation', task_id: '0', draft: { due_at_unix_ms: 0, revision: '1', title: '<script>task</script>', description: 'Write notes', source_message_id: '9007199254740999' }
    } });
  });
  fields.teamId.value = '9007199254740993';
  fields.toUserId.value = '9007199254740995';
  fields.chatType.value = '2';
  fields.draftInstruction.value = '  Extract one task  ';
  assert.equal(await context.prepareTaskDraft(), '9007199254740997');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/v1/teams/9007199254740993/groups/9007199254740995/task-drafts');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer test-token');
  assert.equal(calls[0].options.headers['Idempotency-Key'], fields.draftRequestKey.value);
  assert.equal(JSON.parse(calls[0].options.body).instruction, 'Extract one task');
  assert.ok(Number.isSafeInteger(JSON.parse(calls[0].options.body).instruction_reference_unix_ms));
  assert.equal(calls[1].url, '/api/v1/agent/runs/9007199254740997/draft');
  assert.equal(fields.draftRunID.value, '9007199254740997');
  assert.match(fields.draftResult.textContent, /<script>task<\/script>/);
  assert.equal(fields.btnPrepareDraft.disabled, false);
  assert.equal(fields.btnLoadDraft.disabled, false);
});

test('task draft retry reuses its key and changed instruction gets a new key', async () => {
  const calls = [];
  const { context, fields } = page(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return { ok: false, status: 503 };
    if (calls.length === 2) return reply({ code: 0, data: { run_id: '9' } });
    if (calls.length === 3) return reply({ code: 0, data: { run_id: '9', team_id: '2', group_id: '3', status: 'waiting_confirmation', task_id: '0', draft: { due_at_unix_ms: 0, revision: '1', title: 'Task', description: '', source_message_id: '0' } } });
    if (calls.length === 4) return reply({ code: 0, data: { run_id: '10' } });
    return reply({ code: 0, data: { run_id: '10', team_id: '2', group_id: '3', status: 'waiting_confirmation', task_id: '0', draft: { due_at_unix_ms: 0, revision: '1', title: 'New', description: '', source_message_id: '0' } } });
  });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.draftInstruction.value = 'First';
  await assert.rejects(context.prepareTaskDraft(), /draft prepare HTTP 503/);
  const firstKey = fields.draftRequestKey.value;
  await context.prepareTaskDraft();
  assert.equal(calls[0].options.headers['Idempotency-Key'], calls[1].options.headers['Idempotency-Key']);
  fields.draftInstruction.value = 'Second';
  await context.prepareTaskDraft();
  assert.notEqual(calls[3].options.headers['Idempotency-Key'], firstKey);
  assert.equal(fields.draftRunID.value, '10');
});

test('task draft keeps the run ID when the follow-up read fails', async () => {
  let calls = 0;
  const { context, fields } = page(async () => {
    calls++;
    return calls === 1 ? reply({ code: 0, data: { run_id: '9' } }) : { ok: false, status: 503 };
  });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.draftInstruction.value = 'Extract a task';
  assert.equal(await context.prepareTaskDraft(), '9');
  assert.equal(fields.draftRunID.value, '9');
  assert.match(fields.draftResult.textContent, /Draft prepared; read failed/);
  assert.equal(calls, 2);
});

test('task draft read hides other group and stale responses', async () => {
  let finish;
  const waiting = new Promise(resolve => { finish = resolve; });
  const { context, fields } = page(async () => waiting);
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '2';
  fields.draftRunID.value = '9';
  const read = context.loadTaskDraft();
  fields.toUserId.value = '4';
  finish(reply({ code: 0, data: { run_id: '9', team_id: '2', group_id: '3', status: 'waiting_confirmation', task_id: '0', draft: { due_at_unix_ms: 0, revision: '1', title: 'Old', description: '', source_message_id: '0' } } }));
  await read;
  assert.match(fields.draftResult.textContent, /Context changed/);
  assert.doesNotMatch(fields.draftResult.textContent, /Old/);
  const { context: otherContext, fields: otherFields } = page(async () => reply({ code: 0, data: {
    run_id: '9', team_id: '2', group_id: '3', status: 'waiting_confirmation', task_id: '0',
    draft: { due_at_unix_ms: 0, revision: '1', title: 'Private', description: '', source_message_id: '0' }
  } }));
  otherFields.teamId.value = '2';
  otherFields.toUserId.value = '4';
  otherFields.chatType.value = '2';
  otherFields.draftRunID.value = '9';
  await otherContext.loadTaskDraft();
  assert.match(otherFields.draftResult.textContent, /another team group/);
  assert.doesNotMatch(otherFields.draftResult.textContent, /Private/);
});

test('task draft rejects invalid scope and does not send a request', async () => {
  let calls = 0;
  const { context, fields } = page(async () => { calls++; return reply({ code: 0 }); });
  fields.teamId.value = '2';
  fields.toUserId.value = '3';
  fields.chatType.value = '1';
  fields.draftInstruction.value = 'Task';
  await assert.rejects(context.prepareTaskDraft(), /team group/);
  fields.chatType.value = '2';
  fields.draftInstruction.value = ' ';
  await assert.rejects(context.prepareTaskDraft(), /instruction/);
  fields.draftRunID.value = '9223372036854775808';
  await assert.rejects(context.loadTaskDraft(), /valid run ID/);
  assert.equal(calls, 0);
});

function draftEditPage(fetch) {
  const result = page(fetch);
  result.fields.teamId.value = '2';
  result.fields.toUserId.value = '3';
  result.fields.chatType.value = '2';
  result.fields.draftRunID.value = '9';
  return result;
}

function draftData(title = 'Old title', description = 'Old notes', overrides = {}) {
  return { code: 0, data: { run_id: '9', team_id: '2', group_id: '3', status: 'waiting_confirmation', task_id: '0',
    draft: { due_at_unix_ms: 0, revision: '1', title, description, source_message_id: '0' }, ...overrides } };
}

function assigneeDraft(state = 'ambiguous', id = '0', name = '李四', revision = '1', overrides = {}) {
  const result = draftData('Old title', 'Old notes', overrides);
  Object.assign(result.data.draft, { assignee_name: name, assignee_resolution: state, assignee_id: id, revision });
  return result;
}

function deadlineDraft(due = 0, revision = '1', overrides = {}) {
  const result = draftData('Old title', 'Old notes', overrides);
  result.data.draft.due_at_unix_ms = due;
  result.data.draft.revision = revision;
  return result;
}

function autoDeadlineDraft(resolution = 'parsed', metaOverrides = {}) {
  const reference = Date.UTC(2026, 9, 3, 10), parsed = Date.UTC(2026, 9, 4, 7, 30);
  const none = resolution === 'none', unresolved = resolution === 'needs_input';
  const meta = { text: none ? '' : unresolved ? '明天下午' : '明天15:30', source: none ? 'none' : 'instruction',
    source_message_id: '0', reference_unix_ms: none ? 0 : reference, timezone: 'Asia/Shanghai', resolution,
    reason: unresolved ? 'unsupported_expression' : '', parsed_unix_ms: none || unresolved ? 0 : parsed,
    instruction_reference_unix_ms: reference, ...metaOverrides };
  const due = ['none', 'needs_input', 'unset'].includes(resolution) ? 0 : meta.parsed_unix_ms;
  const result = deadlineDraft(due);
  result.data.draft.deadline = meta;
  return result;
}

function handledDeadline(result, resolution, due, revision) {
  const updated = JSON.parse(JSON.stringify(result));
  updated.data.draft.deadline.resolution = resolution;
  updated.data.draft.due_at_unix_ms = due;
  updated.data.draft.revision = revision;
  return updated;
}

test('parsed deadline displays saved evidence and first reference separately, then confirms the reviewed resolution', async () => {
  const initial = autoDeadlineDraft(), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const result = JSON.parse(JSON.stringify(initial));
    if (url.endsWith('/confirm')) Object.assign(result.data, { status: 'succeeded', task_id: '123' });
    return reply(result);
  });
  await context.loadTaskDraft();
  assert.equal(calls.length, 1);
  assert.match(fields.draftDeadlineEvidence.textContent, /Time expression: 明天15:30/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Source: Instruction/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Interpretation reference: 2026-10-03 18:00 Asia\/Shanghai/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Original parsed candidate: 2026-10-04 15:30/);
  assert.match(fields.draftDeadlineEvidence.textContent, /First instruction submission reference \(saved\): 2026-10-03 18:00/);
  assert.match(fields.draftReferenceSummary.textContent, /does not retrieve/);
  assert.equal(fields.btnConfirmDraft.disabled, false);
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[1].options.body).expected_deadline_resolution, 'parsed');
  assert.equal(JSON.parse(calls[1].options.body).expected_due_at_unix_ms, initial.data.draft.due_at_unix_ms);
});

test('none evidence still displays the original first submission reference and is explicitly reviewed as none', async () => {
  const initial = autoDeadlineDraft('none'), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const result = JSON.parse(JSON.stringify(initial));
    if (calls.length > 1) Object.assign(result.data, { status: 'succeeded', task_id: '123' });
    return reply(result);
  });
  await context.loadTaskDraft();
  assert.match(fields.draftDeadlineEvidence.textContent, /Time expression: \(none\)/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Interpretation reference: Not provided/);
  assert.match(fields.draftDeadlineEvidence.textContent, /First instruction submission reference \(saved\): 2026-10-03 18:00/);
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[1].options.body).expected_deadline_resolution, 'none');
  assert.equal(JSON.parse(calls[1].options.body).expected_due_at_unix_ms, 0);
});

test('message evidence keeps exact source ID, independent effective reference and original candidate after manual override', async () => {
  const initial = autoDeadlineDraft('parsed', { source: 'message', source_message_id: '9007199254740997',
    text: '<script>明天15:30</script>', reference_unix_ms: Date.UTC(2026, 9, 2, 10), parsed_unix_ms: Date.UTC(2026, 9, 3, 7, 30) });
  const selectedDue = Date.UTC(2026, 9, 5, 7, 30), selected = handledDeadline(initial, 'selected', selectedDue, '2'), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const result = JSON.parse(JSON.stringify(calls.length === 1 ? initial : selected));
    if (url.endsWith('/confirm')) Object.assign(result.data, { status: 'succeeded', task_id: '123' });
    return reply(result);
  });
  await context.loadTaskDraft();
  assert.match(fields.draftDeadlineEvidence.textContent, /Source: Message #9007199254740997/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Interpretation reference: 2026-10-02 18:00/);
  assert.match(fields.draftDeadlineEvidence.textContent, /First instruction submission reference \(saved\): 2026-10-03 18:00/);
  assert.match(fields.draftDeadlineEvidence.textContent, /<script>明天15:30<\/script>/);
  fields.draftDueAt.value = '2026-10-05T15:30';
  await context.saveDraftDeadline();
  assert.match(fields.draftDeadlineSummary.textContent, /2026-10-05 15:30/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Original parsed candidate: 2026-10-03 15:30/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Your saved deadline overrides/);
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[2].options.body).expected_deadline_resolution, 'selected');
});

test('needs-input empty time is blocked until explicit zero save changes state and revision; subsequent zero save is noop', async () => {
  const initial = autoDeadlineDraft('needs_input'), unset = handledDeadline(initial, 'unset', 0, '2'), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const result = JSON.parse(JSON.stringify(calls.length === 1 ? initial : unset));
    if (url.endsWith('/confirm')) Object.assign(result.data, { status: 'succeeded', task_id: '123' });
    return reply(result);
  });
  await context.loadTaskDraft();
  assert.equal(fields.draftDueAt.value, '');
  assert.equal(fields.btnConfirmDraft.disabled, true);
  await assert.rejects(context.confirmTaskDraft(), /explicitly save no deadline/);
  assert.equal(calls.length, 1);
  await context.saveDraftDeadline();
  assert.deepEqual(JSON.parse(calls[1].options.body), { due_at_unix_ms: 0, expected_revision: '1' });
  assert.match(fields.draftDeadlineEvidence.textContent, /Original issue: Expression needs a complete date and time/);
  assert.match(fields.draftDeadlineEvidence.textContent, /You explicitly saved no deadline/);
  assert.equal(fields.btnConfirmDraft.disabled, false);
  await context.saveDraftDeadline();
  assert.equal(JSON.parse(calls[2].options.body).expected_revision, '2');
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[3].options.body).expected_deadline_resolution, 'unset');
});

test('needs-input explicit time must be saved before confirmation and retains original unsupported expression', async () => {
  const initial = autoDeadlineDraft('needs_input'), due = Date.UTC(2026, 9, 4, 7, 30), selected = handledDeadline(initial, 'selected', due, '2');
  let calls = 0;
  const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : selected));
  await context.loadTaskDraft();
  fields.draftDueAt.value = '2026-10-04T15:30';
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
  await context.saveDraftDeadline();
  assert.match(fields.draftDeadlineEvidence.textContent, /Time expression: 明天下午/);
  assert.match(fields.draftDeadlineEvidence.textContent, /Original issue: Expression needs a complete date and time/);
  assert.equal(fields.btnConfirmDraft.disabled, false);
});

test('saving an unchanged parsed candidate explicitly selects it and increments revision once', async () => {
  const initial = autoDeadlineDraft(), selected = handledDeadline(initial, 'selected', initial.data.draft.due_at_unix_ms, '2'), calls = [];
  const { context } = draftEditPage(async (url, options) => { calls.push({ url, options }); return reply(calls.length === 1 ? initial : selected); });
  await context.loadTaskDraft();
  await context.saveDraftDeadline();
  await context.saveDraftDeadline();
  assert.equal(JSON.parse(calls[1].options.body).expected_revision, '1');
  assert.equal(JSON.parse(calls[2].options.body).expected_revision, '2');
});

test('deadline metadata must contain all nine fields and valid UTF, IDs, references, states and candidate relationships', async () => {
  const invalid = [data => { data.draft.deadline = {}; }, data => { data.draft.deadline = null; },
    data => { delete data.draft.deadline.instruction_reference_unix_ms; }, data => { data.draft.deadline.extra = true; },
    data => { data.draft.deadline.text = '\ud800'; }, data => { data.draft.deadline.text = ' padded '; },
    data => { data.draft.deadline.text = 'a'.repeat(201); }, data => { data.draft.deadline.timezone = 'UTC'; },
    data => { data.draft.deadline.source = 'unknown'; }, data => { data.draft.deadline.source_message_id = 9; },
    data => { data.draft.deadline.source_message_id = '01'; }, data => { data.draft.deadline.reference_unix_ms += 1; },
    data => { data.draft.deadline.instruction_reference_unix_ms = -1; }, data => { data.draft.deadline.parsed_unix_ms = '1'; },
    data => { data.draft.deadline.parsed_unix_ms = 253402300800000; }, data => { data.draft.deadline.reason = 'unknown'; },
    data => { data.draft.deadline.resolution = ''; }, data => { data.draft.deadline.resolution = 'none'; },
    data => { data.draft.deadline.resolution = 'needs_input'; }, data => { data.draft.deadline.reason = 'invalid_date'; },
    data => { data.draft.due_at_unix_ms += 1; }, data => { delete data.draft.due_at_unix_ms; },
    data => { data.draft.deadline.source = 'message'; }, data => { data.draft.deadline.source = 'none'; }];
  for (const change of invalid) {
    const result = autoDeadlineDraft(); change(result.data);
    const { context, fields } = draftEditPage(async () => reply(result));
    await assert.rejects(context.loadTaskDraft(), /invalid response/);
    await assert.rejects(context.confirmTaskDraft(), /load/);
    assert.equal(fields.draftDeadlineEvidence.textContent, '');
  }
});

test('absolute evidence without instruction reference and none manual selection remain shape-valid without fabricated reference', async () => {
  const absolute = autoDeadlineDraft('parsed', { text: '2026-10-04 15:30', reference_unix_ms: 0, instruction_reference_unix_ms: 0 });
  const { context, fields } = draftEditPage(async () => reply(absolute));
  await context.loadTaskDraft();
  assert.match(fields.draftDeadlineEvidence.textContent, /Interpretation reference: Not provided/);
  assert.equal(fields.btnConfirmDraft.disabled, false);
  const none = autoDeadlineDraft('none');
  const selected = handledDeadline(none, 'selected', Date.UTC(2026, 9, 4, 7, 30), '2');
  let calls = 0;
  const pageResult = draftEditPage(async () => reply(++calls === 1 ? none : selected));
  await pageResult.context.loadTaskDraft();
  pageResult.fields.draftDueAt.value = '2026-10-04T15:30';
  await pageResult.context.saveDraftDeadline();
  assert.match(pageResult.fields.draftDeadlineEvidence.textContent, /Time expression: \(none\)/);
});

test('missing-reference evidence requires an instruction with no saved reference and remains reviewable after explicit clearing', async () => {
  const initial = autoDeadlineDraft('needs_input', { reason: 'missing_reference', reference_unix_ms: 0, instruction_reference_unix_ms: 0 });
  const unset = handledDeadline(initial, 'unset', 0, '2');
  let calls = 0;
  const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : unset));
  await context.loadTaskDraft();
  assert.match(fields.draftDeadlineEvidence.textContent, /Original instruction reference is missing/);
  await context.saveDraftDeadline();
  assert.equal(fields.btnConfirmDraft.disabled, false);
  for (const invalid of [autoDeadlineDraft('needs_input', { reason: 'missing_reference' }),
    autoDeadlineDraft('needs_input', { reason: 'missing_reference', source: 'message', source_message_id: '8' })]) {
    const next = draftEditPage(async () => reply(invalid));
    await assert.rejects(next.context.loadTaskDraft(), /invalid response/);
  }
});

test('every original evidence field is immutable during deadline saves and resolution must represent the requested choice', async () => {
  for (const field of deadlineMetadataFieldNames()) {
    const initial = autoDeadlineDraft(), next = handledDeadline(initial, 'selected', initial.data.draft.due_at_unix_ms, '2');
    if (field === 'resolution') next.data.draft.deadline.resolution = 'parsed';
    else if (field === 'source_message_id') { next.data.draft.deadline.source = 'message'; next.data.draft.deadline.source_message_id = '8'; }
    else if (field === 'reference_unix_ms' || field === 'instruction_reference_unix_ms') {
      next.data.draft.deadline.reference_unix_ms += 1; next.data.draft.deadline.instruction_reference_unix_ms += 1;
    } else if (typeof next.data.draft.deadline[field] === 'number') next.data.draft.deadline[field] += 1;
    else next.data.draft.deadline[field] += 'changed';
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : next));
    await context.loadTaskDraft();
    await assert.rejects(context.saveDraftDeadline(), /invalid deadline save response/);
    assert.match(fields.draftDeadlineEvidence.textContent, /Original parsed candidate: 2026-10-04 15:30/);
    await assert.rejects(context.confirmTaskDraft(), /load/);
  }
});

function deadlineMetadataFieldNames() {
  return ['text', 'source', 'source_message_id', 'reference_unix_ms', 'timezone', 'resolution', 'reason', 'parsed_unix_ms', 'instruction_reference_unix_ms'];
}

test('text and assignee saves cannot alter or omit interpretation evidence, even if the new object is independently valid', async () => {
  for (const action of ['saveTaskDraft', 'saveDraftAssignee']) {
    for (const omit of [false, true]) {
      const initial = autoDeadlineDraft();
      Object.assign(initial.data.draft, { assignee_id: '8', assignee_name: '李四', assignee_resolution: 'matched' });
      const changed = JSON.parse(JSON.stringify(initial)); changed.data.draft.revision = '2';
      if (action === 'saveTaskDraft') changed.data.draft.title = 'Edited';
      else Object.assign(changed.data.draft, { assignee_id: '0', assignee_resolution: 'unassigned' });
      if (omit) delete changed.data.draft.deadline;
      else changed.data.draft.deadline.text = '2026-10-04 15:30';
      let calls = 0;
      const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : changed));
      await context.loadTaskDraft();
      if (action === 'saveTaskDraft') fields.draftEditTitle.value = 'Edited'; else fields.draftAssigneeSelect.value = '0';
      await assert.rejects(context[action](), /invalid/);
      assert.match(fields.draftDeadlineEvidence.textContent, /Time expression: 明天15:30/);
      await assert.rejects(context.confirmTaskDraft(), /load/);
    }
  }
});

test('confirmation and reply cannot change saved interpretation resolution, candidate or first instruction reference', async () => {
  for (const mode of ['confirm', 'reply']) {
    for (const field of ['resolution', 'parsed_unix_ms', 'instruction_reference_unix_ms']) {
      const initial = handledDeadline(autoDeadlineDraft(), 'selected', Date.UTC(2026, 9, 5, 7, 30), '2');
      if (mode === 'reply') Object.assign(initial.data, { status: 'succeeded', task_id: '123', reply_status: 'pending', reply_msg_id: 'bot-task:9' });
      const changed = JSON.parse(JSON.stringify(initial));
      Object.assign(changed.data, { status: 'succeeded', task_id: '123', ...(mode === 'reply' ? { reply_status: 'accepted' } : {}) });
      if (field === 'resolution') { changed.data.draft.deadline.resolution = 'parsed'; changed.data.draft.deadline.parsed_unix_ms = changed.data.draft.due_at_unix_ms; }
      else if (field === 'instruction_reference_unix_ms') { changed.data.draft.deadline.reference_unix_ms += 1; changed.data.draft.deadline.instruction_reference_unix_ms += 1; }
      else changed.data.draft.deadline.parsed_unix_ms += 1;
      let calls = 0;
      const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : changed));
      await context.loadTaskDraft();
      await assert.rejects(context[mode === 'reply' ? 'retryTaskReply' : 'confirmTaskDraft'](), /invalid/);
      assert.match(fields.draftDeadlineEvidence.textContent, /Your saved deadline overrides/);
    }
  }
});

test('needs-input conflict retains pending time but requires the latest evidence/version before manual retry', async () => {
  const initial = autoDeadlineDraft('needs_input'), latest = JSON.parse(JSON.stringify(initial));
  latest.data.draft.revision = '3'; latest.data.draft.deadline.text = '后天下午';
  const selected = handledDeadline(latest, 'selected', Date.UTC(2026, 9, 5, 7, 30), '4'), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return calls.length === 2 ? { ok: false, status: 409 } : reply(calls.length === 1 ? initial : calls.length === 3 ? latest : selected);
  });
  await context.loadTaskDraft(); fields.draftDueAt.value = '2026-10-05T15:30';
  await assert.rejects(context.saveDraftDeadline(), /HTTP 409/);
  await assert.rejects(context.confirmTaskDraft(), /load/);
  await context.loadTaskDraft();
  assert.equal(fields.draftDueAt.value, '2026-10-05T15:30');
  assert.match(fields.draftDeadlineEvidence.textContent, /Time expression: 后天下午/);
  await context.saveDraftDeadline();
  assert.equal(JSON.parse(calls[3].options.body).expected_revision, '3');
});

test('stale evidence reads are ignored after a context epoch change and legacy deadline objects are never synthesized', async () => {
  let finish;
  const pending = new Promise(resolve => { finish = resolve; });
  const { context, fields } = draftEditPage(async () => pending);
  const work = context.loadTaskDraft();
  fields.token.value = 'changed'; context.clearTaskDraftResult(); fields.token.value = 'test-token';
  finish(reply(autoDeadlineDraft()));
  await work;
  assert.equal(fields.draftDeadlineEvidence.textContent, '');
  const calls = [], legacy = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return reply(deadlineDraft(0, '1', calls.length > 1 ? { status: 'succeeded', task_id: '123' } : {}));
  });
  await legacy.context.loadTaskDraft();
  assert.match(legacy.fields.draftDeadlineEvidence.textContent, /legacy draft/);
  await legacy.context.confirmTaskDraft();
  assert.equal(Object.hasOwn(JSON.parse(calls[1].options.body), 'expected_deadline_resolution'), false);
});

test('deadline controls are present, explicitly use Shanghai and preserve millisecond input precision', () => {
  for (const id of ['draftDeadlineSummary', 'draftDueAt', 'btnSaveDraftDeadline', 'btnClearDraftDeadline']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  assert.match(html, /id="draftDueAt" step="0\.001"/);
  assert.match(html, /Due \(Asia\/Shanghai\)/);
});

test('draft deadlines use Shanghai independent of browser timezone and confirm the newly saved instant', async () => {
  const prior = process.env.TZ;
  process.env.TZ = 'America/New_York';
  try {
    const due = Date.UTC(2026, 9, 4, 7, 30), calls = [];
    const { context, fields } = draftEditPage(async (url, options) => {
      calls.push({ url, options });
      return reply(calls.length === 1 ? deadlineDraft(0, '9007199254740993') : deadlineDraft(due, '9007199254740994',
        url.endsWith('/confirm') ? { status: 'succeeded', task_id: '123' } : {}));
    });
    await context.loadTaskDraft();
    fields.draftDueAt.value = '2026-10-04T15:30';
    context.refreshTaskDraftControls();
    await assert.rejects(context.confirmTaskDraft(), /save your changes/);
    await context.saveDraftDeadline();
    assert.deepEqual(JSON.parse(calls[1].options.body), { due_at_unix_ms: due, expected_revision: '9007199254740993' });
    assert.equal(calls[1].url, '/api/v1/agent/runs/9/draft/deadline');
    assert.match(fields.draftDeadlineSummary.textContent, /2026-10-04 15:30/);
    assert.match(fields.draftDeadlineSummary.textContent, /2026-10-04T07:30:00\.000Z/);
    await context.confirmTaskDraft();
    assert.equal(JSON.parse(calls[2].options.body).expected_due_at_unix_ms, due);
    assert.equal(JSON.parse(calls[2].options.body).expected_revision, '9007199254740994');
  } finally { if (prior === undefined) delete process.env.TZ; else process.env.TZ = prior; }
});

test('explicit deadline clear changes only the input until save and confirms numeric zero', async () => {
  const original = Date.UTC(2026, 9, 4, 7, 30, 12, 345), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return reply(calls.length === 1 ? deadlineDraft(original) : deadlineDraft(0, '2',
      url.endsWith('/confirm') ? { status: 'succeeded', task_id: '123' } : {}));
  });
  await context.loadTaskDraft();
  context.clearDraftDeadlineInput();
  assert.equal(fields.draftDueAt.value, '');
  assert.match(fields.draftDeadlineSummary.textContent, /15:30:12\.345/);
  assert.equal(calls.length, 1);
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
  await context.saveDraftDeadline();
  assert.equal(JSON.parse(calls[1].options.body).due_at_unix_ms, 0);
  assert.match(fields.draftDeadlineSummary.textContent, /Not set/);
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[2].options.body).expected_due_at_unix_ms, 0);
});

test('old exact seconds and milliseconds survive reading, noop saving and confirmation', async () => {
  for (const due of [Date.UTC(2026, 9, 4, 7, 30, 12, 345), 1, 253402300799999,
    Date.UTC(1991, 8, 14, 16, 30, 7, 123)]) {
    const calls = [];
    const { context, fields } = draftEditPage(async (url, options) => {
      calls.push({ url, options });
      return reply(deadlineDraft(due, '1', url.endsWith('/confirm') ? { status: 'succeeded', task_id: '123' } : {}));
    });
    await context.loadTaskDraft();
    assert.equal(fields.draftDueAt.value, context.shanghaiDeadlineInput(due));
    await context.saveDraftDeadline();
    assert.deepEqual(JSON.parse(calls[1].options.body), { due_at_unix_ms: due, expected_revision: '1' });
    await context.confirmTaskDraft();
    assert.equal(JSON.parse(calls[2].options.body).expected_due_at_unix_ms, due);
  }
});

test('Shanghai parser rejects illegal dates, historical DST gaps and duplicated local times before fetch', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => { calls++; return reply(deadlineDraft()); });
  await context.loadTaskDraft();
  for (const value of ['2026-02-30T15:30', '2026-13-01T15:30', '2026-10-01T24:00', '2026-10-01T12:60',
    '2026-10-01T12:30:60', '2026-10-01T12:30:01.1234', '1969-12-31T23:59', '10001-01-01T00:00',
    '1991-04-14T02:30', '1991-09-15T01:30']) {
    fields.draftDueAt.value = value;
    await assert.rejects(context.saveDraftDeadline(), /invalid.*Shanghai/);
  }
  fields.draftDueAt.value = '';
  fields.draftDueAt.validity = { badInput: true };
  await assert.rejects(context.saveDraftDeadline(), /invalid.*Shanghai/);
  assert.equal(calls, 1);
  assert.equal(context.parseShanghaiDeadline('1991-07-01T15:30'), Date.UTC(1991, 6, 1, 6, 30));
  assert.equal(context.parseShanghaiDeadline('2026-10-04T15:30:01.7'), Date.UTC(2026, 9, 4, 7, 30, 1, 700));
});

test('legacy responses without deadline remain readable and reject every draft mutation including replies', async () => {
  for (const done of [false, true]) {
    const data = deadlineDraft(0, '1', done ? { status: 'succeeded', task_id: '123', reply_status: 'pending', reply_msg_id: 'bot-task:9' } : {});
    delete data.data.draft.due_at_unix_ms;
    let calls = 0;
    const { context, fields } = draftEditPage(async () => { calls++; return reply(data); });
    await context.loadTaskDraft();
    assert.match(fields.draftDeadlineSummary.textContent, /read-only/);
    for (const action of ['saveTaskDraft', 'saveDraftAssignee', 'saveDraftDeadline', 'confirmTaskDraft', 'retryTaskReply', 'loadDraftMembers']) {
      await assert.rejects(context[action](), /load/);
    }
    assert.equal(calls, 1);
  }
});

test('malformed deadline values never open draft writes', async () => {
  for (const due of [null, '0', -1, 1.5, 253402300800000, Infinity]) {
    const { context, fields } = draftEditPage(async () => reply(deadlineDraft(due)));
    await assert.rejects(context.loadTaskDraft(), /invalid response/);
    assert.equal(fields.btnSaveDraftDeadline.disabled, true);
  }
});

test('deadline conflict retains input and requires reading the new version before explicit retry', async () => {
  const due = Date.UTC(2026, 9, 4, 7, 30), calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(deadlineDraft());
    if (calls.length === 2) return { ok: false, status: 409 };
    if (calls.length === 3) return reply(deadlineDraft(Date.UTC(2026, 9, 3, 7, 30), '3'));
    return reply(deadlineDraft(due, '4'));
  });
  await context.loadTaskDraft();
  fields.draftDueAt.value = '2026-10-04T15:30';
  await assert.rejects(context.saveDraftDeadline(), /HTTP 409.*retained/);
  await assert.rejects(context.saveDraftDeadline(), /load/);
  await assert.rejects(context.confirmTaskDraft(), /load/);
  await context.loadTaskDraft();
  assert.equal(fields.draftDueAt.value, '2026-10-04T15:30');
  await context.saveDraftDeadline();
  assert.equal(JSON.parse(calls[3].options.body).expected_revision, '3');
});

test('deadline save retains newer typing, text and assignee edits while advancing the saved version', async () => {
  let finish, calls = 0;
  const due = Date.UTC(2026, 9, 4, 7, 30), pending = new Promise(resolve => { finish = resolve; });
  const initial = assigneeDraft('matched', '8');
  const saved = assigneeDraft('matched', '8', '李四', '2'); saved.data.draft.due_at_unix_ms = due;
  const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(initial) : pending);
  await context.loadTaskDraft();
  fields.draftDueAt.value = '2026-10-04T15:30';
  const work = context.saveDraftDeadline();
  fields.draftDueAt.value = '2026-10-05T16:30';
  fields.draftEditTitle.value = 'Unsaved title';
  fields.draftAssigneeSelect.value = '0';
  finish(reply(saved));
  await work;
  assert.equal(fields.draftDueAt.value, '2026-10-05T16:30');
  assert.equal(fields.draftEditTitle.value, 'Unsaved title');
  assert.equal(fields.draftAssigneeSelect.value, '0');
  assert.match(fields.draftDeadlineSummary.textContent, /2026-10-04 15:30/);
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
});

test('other draft saves retain unsaved deadline input and preserve the saved UTC instant', async () => {
  const due = Date.UTC(2026, 9, 4, 7, 30);
  let calls = 0;
  const initial = assigneeDraft('matched', '8'); initial.data.draft.due_at_unix_ms = due;
  const textResult = assigneeDraft('matched', '8', '李四', '2'); textResult.data.draft.due_at_unix_ms = due; textResult.data.draft.title = 'Edited';
  const assigneeResult = assigneeDraft('unassigned', '0', '李四', '3'); assigneeResult.data.draft.due_at_unix_ms = due; assigneeResult.data.draft.title = 'Edited';
  const { context, fields } = draftEditPage(async () => reply([initial, textResult, assigneeResult][calls++]));
  await context.loadTaskDraft();
  fields.draftDueAt.value = '';
  fields.draftEditTitle.value = 'Edited';
  await context.saveTaskDraft();
  assert.equal(fields.draftDueAt.value, '');
  fields.draftAssigneeSelect.value = '0';
  await context.saveDraftAssignee();
  assert.equal(fields.draftDueAt.value, '');
  assert.match(fields.draftDeadlineSummary.textContent, /15:30/);
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
});

test('deadline saving excludes every concurrent draft operation and clear action', async () => {
  let finish, calls = 0;
  const pending = new Promise(resolve => { finish = resolve; });
  const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(deadlineDraft()) : pending);
  await context.loadTaskDraft();
  fields.draftDueAt.value = '2026-10-04T15:30';
  fields.draftInstruction.value = 'Extract';
  const work = context.saveDraftDeadline();
  for (const action of ['saveDraftDeadline', 'saveDraftAssignee', 'saveTaskDraft', 'confirmTaskDraft', 'loadTaskDraft', 'loadDraftMembers', 'prepareTaskDraft', 'retryTaskReply']) {
    await assert.rejects(context[action](), /load|progress/);
  }
  assert.throws(() => context.clearDraftDeadlineInput(), /load/);
  assert.equal(calls, 2);
  finish(reply(deadlineDraft(Date.UTC(2026, 9, 4, 7, 30), '2')));
  await work;
});

test('maximum draft version allows exact noop deadline saves but rejects changed time before fetch', async () => {
  let calls = 0;
  const due = Date.UTC(2026, 9, 4, 7, 30), revision = '9223372036854775807';
  const { context, fields } = draftEditPage(async () => { calls++; return reply(deadlineDraft(due, revision)); });
  await context.loadTaskDraft();
  await context.saveDraftDeadline();
  fields.draftDueAt.value = '';
  await assert.rejects(context.saveDraftDeadline(), /version is exhausted/);
  assert.equal(calls, 2);
});

test('deadline save success and failure ignore every changed context including restoration', async () => {
  for (const field of ['token', 'teamId', 'toUserId', 'draftRunID']) {
    for (const fails of [false, true]) {
      let finish, calls = 0;
      const pending = new Promise((resolve, reject) => { finish = fails ? reject : resolve; });
      const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(deadlineDraft()) : pending);
      await context.loadTaskDraft();
      fields.draftDueAt.value = '2026-10-04T15:30';
      const work = context.saveDraftDeadline(), before = fields[field].value;
      fields[field].value = 'changed'; context.clearTaskDraftResult(); fields[field].value = before;
      finish(fails ? new Error('network') : reply(deadlineDraft(Date.UTC(2026, 9, 4, 7, 30), '2')));
      await work;
      assert.equal(fields.draftDeadlineSummary.textContent, '');
      assert.equal(fields.btnSaveDraftDeadline.disabled, true);
      assert.equal(fields.btnConfirmDraft.disabled, true);
    }
  }
});

test('deadline save rejects altered fields, missing time and incorrect version without displaying success', async () => {
  const due = Date.UTC(2026, 9, 4, 7, 30);
  for (const change of [data => { data.draft.title = 'Other'; }, data => { data.draft.assignee_id = '8'; },
    data => { data.draft.source_message_id = '8'; }, data => { data.draft.due_at_unix_ms = 0; },
    data => { delete data.draft.due_at_unix_ms; }, data => { data.draft.revision = '1'; },
    data => { data.draft.revision = '3'; }, data => { data.status = 'creating'; }, data => { data.group_id = '4'; }]) {
    const result = deadlineDraft(due, '2'); change(result.data);
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? deadlineDraft() : result));
    await context.loadTaskDraft(); fields.draftDueAt.value = '2026-10-04T15:30';
    await assert.rejects(context.saveDraftDeadline(), /invalid deadline save response/);
    assert.equal(fields.draftDueAt.value, '2026-10-04T15:30');
    await assert.rejects(context.confirmTaskDraft(), /load/);
  }
});

test('creating and successful deadlines are frozen; confirmation and reply cannot replace their UTC values', async () => {
  const due = Date.UTC(2026, 9, 4, 7, 30, 12, 345);
  for (const mode of ['confirm', 'reply']) {
    const frozen = deadlineDraft(due, '1', mode === 'confirm' ? { status: 'creating' } :
      { status: 'succeeded', task_id: '123', reply_status: 'pending', reply_msg_id: 'bot-task:9' });
    const changed = deadlineDraft(due + 1, '1', { status: 'succeeded', task_id: '123',
      ...(mode === 'reply' ? { reply_status: 'accepted', reply_msg_id: 'bot-task:9' } : {}) });
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? frozen : changed));
    await context.loadTaskDraft();
    assert.equal(fields.draftDueAt.disabled, true);
    await assert.rejects(context.saveDraftDeadline(), /load/);
    await assert.rejects(context[mode === 'confirm' ? 'confirmTaskDraft' : 'retryTaskReply'](), /invalid/);
    assert.match(fields.draftDeadlineSummary.textContent, /12\.345/);
    assert.equal(fields.btnSaveDraftDeadline.disabled, true);
  }
});

function memberPage(members, next = '0') {
  return reply({ code: 0, data: { members: members.map(([id, name]) => ({ user_id: id, username: name, nickname: '', role: 1 })),
    next_after_user_id: next } });
}

test('assignee controls exist in the actual page markup', () => {
  for (const id of ['draftAssigneeSummary', 'draftMemberHint', 'draftAssigneeSelect', 'btnLoadDraftMembers', 'btnMoreDraftMembers', 'btnSaveDraftAssignee']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
});

test('unique match displays the real saved ID and confirms its exact reviewed value without auto-loading members', async () => {
  const calls = [], id = '9007199254740995';
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return reply(assigneeDraft('matched', id, '<script>李四</script>', '1', calls.length === 1 ? {} : { status: 'succeeded', task_id: '123' }));
  });
  await context.loadTaskDraft();
  assert.equal(calls.length, 1);
  assert.match(fields.draftAssigneeSummary.textContent, /Original name: <script>李四<\/script>/);
  assert.match(fields.draftAssigneeSummary.textContent, new RegExp('#' + id));
  assert.equal(fields.draftAssigneeSelect.value, id);
  assert.equal(fields.btnConfirmDraft.disabled, false);
  assert.equal(fields.btnSaveDraftAssignee.disabled, true); // Saved ID is not a loaded selectable directory entry.
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[1].options.body).expected_assignee_id, id);
});

test('unresolved assignee states reject direct confirmation and distinguish placeholder from explicit zero', async () => {
  for (const state of ['not_found', 'ambiguous', 'truncated']) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => { calls++; return reply(assigneeDraft(state)); });
    await context.loadTaskDraft();
    assert.equal(fields.draftAssigneeSelect.value, '');
    assert.equal(fields.btnConfirmDraft.disabled, true);
    await assert.rejects(context.confirmTaskDraft(), /select and save/);
    await assert.rejects(context.saveDraftAssignee(), /choose Unassigned/);
    fields.draftAssigneeSelect.value = '0';
    context.refreshTaskDraftControls();
    assert.equal(fields.btnSaveDraftAssignee.disabled, false);
    await assert.rejects(context.confirmTaskDraft(), /save your changes/);
    assert.equal(calls, 1);
  }
});

test('explicit unassigned selection retains original name, increments version and becomes confirmable', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return reply(calls.length === 1 ? assigneeDraft() : assigneeDraft('unassigned', '0', '李四', '2',
      calls.length === 3 ? { status: 'succeeded', task_id: '123' } : {}));
  });
  await context.loadTaskDraft();
  fields.draftAssigneeSelect.value = '0';
  await context.saveDraftAssignee();
  assert.equal(calls[1].url, '/api/v1/agent/runs/9/draft/assignee');
  assert.deepEqual(JSON.parse(calls[1].options.body), { assignee_id: '0', expected_revision: '1' });
  assert.match(fields.draftAssigneeSummary.textContent, /Original name: 李四/);
  assert.match(fields.draftAssigneeSummary.textContent, /Explicitly unassigned/);
  assert.equal(fields.btnConfirmDraft.disabled, false);
  await context.confirmTaskDraft();
  assert.deepEqual(JSON.parse(calls[2].options.body), { expected_title: 'Old title', expected_description: 'Old notes',
    expected_revision: '2', expected_assignee_id: '0', expected_due_at_unix_ms: 0 });
});

test('member directory loads successive exact string cursors and saves a real later-page member', async () => {
  const first = '9007199254740993', second = '9007199254740995', calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(assigneeDraft('ambiguous', '0', '李四', '9007199254740993'));
    if (calls.length === 2) return memberPage([[first, '<script>first</script>']], first);
    if (calls.length === 3) return memberPage([[second, 'second']]);
    return reply(assigneeDraft('selected', second, '李四', '9007199254740994'));
  });
  await context.loadTaskDraft();
  await context.loadDraftMembers();
  assert.equal(fields.btnMoreDraftMembers.disabled, false);
  assert.match(fields.draftMemberHint.textContent, /not the full directory/);
  assert.equal(fields.draftAssigneeSelect.value, '');
  assert.match(fields.draftAssigneeSelect.options.find(option => option.value === first).textContent, /<script>first<\/script>/);
  await context.loadDraftMembers(true);
  assert.equal(calls[2].url, '/api/v1/teams/2/members?after_user_id=' + first + '&limit=100');
  assert.equal(fields.btnMoreDraftMembers.disabled, true);
  assert.deepEqual(fields.draftAssigneeSelect.options.map(option => option.value), ['', '0', first, second]);
  fields.draftAssigneeSelect.value = second;
  await context.saveDraftAssignee();
  assert.deepEqual(JSON.parse(calls[3].options.body), { assignee_id: second, expected_revision: '9007199254740993' });
  assert.match(fields.draftAssigneeSummary.textContent, /second #9007199254740995/);
  assert.match(fields.draftAssigneeSummary.textContent, /Original name: 李四/);
});

test('failed or malformed next directory page retains selection and retries the identical cursor', async () => {
  for (const failure of [{ ok: false, status: 503 }, reply({ code: 0, data: { members: [], next_after_user_id: '7' } }),
    memberPage([['5', 'duplicate']], '5')]) {
    const calls = [];
    const { context, fields } = draftEditPage(async url => {
      calls.push(url);
      if (calls.length === 1) return reply(assigneeDraft());
      if (calls.length === 2) return memberPage([['5', 'known']], '5');
      if (calls.length === 3) return failure;
      return memberPage([['7', 'later']]);
    });
    await context.loadTaskDraft();
    await context.loadDraftMembers();
    fields.draftAssigneeSelect.value = '5';
    await assert.rejects(context.loadDraftMembers(true), /directory/);
    assert.equal(fields.draftAssigneeSelect.value, '5');
    assert.equal(fields.btnMoreDraftMembers.disabled, false);
    await context.loadDraftMembers(true);
    assert.equal(calls[2], calls[3]);
  }
});

test('directory rejects malformed IDs, cursors and unordered members before displaying choices', async () => {
  for (const bad of [memberPage([['0', 'bad']]), memberPage([[9007199254740992, 'numeric']]),
    memberPage([['9223372036854775808', 'overflow']]), memberPage([['5', 'five'], ['4', 'four']]),
    memberPage([['5', 'five']], '6'), memberPage([['5', 'five']], 5)]) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(assigneeDraft('matched', '8')) : bad);
    await context.loadTaskDraft();
    await assert.rejects(context.loadDraftMembers(), /directory/);
    assert.equal(fields.draftAssigneeSelect.value, '8');
    assert.equal(fields.draftAssigneeSelect.options.length, 3);
  }
});

test('assignee save never accepts arbitrary ID input or the saved-only ID as a directory selection', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => { calls++; return reply(assigneeDraft('matched', '8')); });
  await context.loadTaskDraft();
  for (const value of ['8', '99', '-1', '00', 8]) {
    fields.draftAssigneeSelect.value = value;
    await assert.rejects(context.saveDraftAssignee(), /choose Unassigned/);
  }
  assert.equal(calls, 1);
});

test('assignee conflicts retain unsaved choice and require rereading the latest version before resubmission', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(assigneeDraft());
    if (calls.length === 2) return { ok: false, status: 409 };
    if (calls.length === 3) return reply(assigneeDraft('matched', '8', '李四', '3'));
    return reply(assigneeDraft('unassigned', '0', '李四', '4'));
  });
  await context.loadTaskDraft();
  fields.draftAssigneeSelect.value = '0';
  await assert.rejects(context.saveDraftAssignee(), /HTTP 409.*selection is retained/);
  assert.equal(fields.draftAssigneeSelect.value, '0');
  await assert.rejects(context.saveDraftAssignee(), /load the current/);
  await assert.rejects(context.confirmTaskDraft(), /load the current/);
  await context.loadTaskDraft();
  assert.equal(fields.draftAssigneeSelect.value, '0');
  await context.saveDraftAssignee();
  assert.equal(JSON.parse(calls[3].options.body).expected_revision, '3');
});

test('text saves retain unsaved assignee selection and assignee saves retain unsaved text', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(assigneeDraft('matched', '8'));
    if (calls.length === 2) {
      const result = assigneeDraft('matched', '8', '李四', '2');
      result.data.draft.title = 'Saved title';
      return reply(result);
    }
    const result = assigneeDraft('unassigned', '0', '李四', '3');
    result.data.draft.title = 'Saved title';
    return reply(result);
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'Saved title';
  fields.draftAssigneeSelect.value = '0';
  await context.saveTaskDraft();
  assert.equal(fields.draftAssigneeSelect.value, '0');
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
  fields.draftEditDescription.value = 'Still unsaved';
  await context.saveDraftAssignee();
  assert.equal(fields.draftEditDescription.value, 'Still unsaved');
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
});

test('assignee mutation and directory reads exclude all other draft operations while in flight', async () => {
  for (const action of ['saveDraftAssignee', 'loadDraftMembers']) {
    let finish, calls = 0;
    const pending = new Promise(resolve => { finish = resolve; });
    const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(assigneeDraft()) : pending);
    await context.loadTaskDraft();
    fields.draftAssigneeSelect.value = '0';
    fields.draftInstruction.value = 'Extract';
    const work = context[action]();
    for (const blocked of ['loadTaskDraft', 'saveTaskDraft', 'saveDraftAssignee', 'loadDraftMembers', 'confirmTaskDraft', 'prepareTaskDraft', 'retryTaskReply']) {
      await assert.rejects(context[blocked](), /progress|load/);
    }
    assert.equal(calls, 2);
    finish(action === 'saveDraftAssignee' ? reply(assigneeDraft('unassigned', '0', '李四', '2')) : memberPage([['8', 'member']]));
    await work;
  }
});

test('assignee and directory responses ignore context changes even after values are restored', async () => {
  for (const action of ['saveDraftAssignee', 'loadDraftMembers']) {
    for (const fails of [false, true]) {
      let finish, calls = 0;
      const pending = new Promise((resolve, reject) => { finish = fails ? reject : resolve; });
      const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(assigneeDraft()) : pending);
      await context.loadTaskDraft();
      fields.draftAssigneeSelect.value = '0';
      const work = context[action]();
      fields.teamId.value = '9';
      context.clearTaskDraftResult();
      fields.teamId.value = '2';
      finish(fails ? new Error('network lost') : action === 'saveDraftAssignee' ? reply(assigneeDraft('unassigned', '0', '李四', '2')) : memberPage([['8', 'member']]));
      await work;
      assert.equal(fields.draftAssigneeSummary.textContent, '');
      assert.equal(fields.draftAssigneeSelect.value, '');
      assert.equal(fields.btnConfirmDraft.disabled, true);
      assert.equal(fields.btnSaveDraftAssignee.disabled, true);
    }
  }
});

test('assignee response invalid combinations reject writes rather than treating them as unassigned', async () => {
  const invalid = [assigneeDraft('matched'), assigneeDraft('ambiguous', '8'), assigneeDraft('selected'),
    assigneeDraft('unassigned', '8'), assigneeDraft('none', '0', '李四'), assigneeDraft('unknown'),
    assigneeDraft('matched', '8', ''), assigneeDraft('selected', '8', ' padded '), assigneeDraft('selected', '8', 'x'.repeat(65)),
    assigneeDraft('selected', 8), assigneeDraft('selected', '9223372036854775808')];
  const missing = assigneeDraft('none', '0', ''); delete missing.data.draft.assignee_id; invalid.push(missing);
  const partial = assigneeDraft(); delete partial.data.draft.assignee_resolution; invalid.push(partial);
  for (const response of invalid) {
    const { context, fields } = draftEditPage(async () => reply(response));
    await assert.rejects(context.loadTaskDraft(), /invalid response/);
    await assert.rejects(context.confirmTaskDraft(), /load the current/);
    assert.equal(fields.btnConfirmDraft.disabled, true);
  }
});

test('missing revision, frozen drafts and wrong scopes reject member reads and selection before fetch', async () => {
  for (const response of [assigneeDraft('selected', '8', '', undefined),
    assigneeDraft('selected', '8', '', '1', { status: 'creating' }),
    assigneeDraft('selected', '8', '', '1', { status: 'succeeded', task_id: '123' })]) {
    if (response.data.status === 'waiting_confirmation') delete response.data.draft.revision;
    let calls = 0;
    const { context, fields } = draftEditPage(async () => { calls++; return reply(response); });
    await context.loadTaskDraft();
    fields.draftAssigneeSelect.value = '0';
    await assert.rejects(context.saveDraftAssignee(), /load the current/);
    await assert.rejects(context.loadDraftMembers(), /load the current/);
    assert.equal(calls, 1);
  }
  const { context, fields } = draftEditPage(async () => reply(assigneeDraft('selected', '8', '')));
  await context.loadTaskDraft();
  fields.token.value = 'different-token';
  await assert.rejects(context.loadDraftMembers(), /load the current/);
});

test('malformed assignee save results preserve pending choice and require a fresh read', async () => {
  const invalid = [assigneeDraft('unassigned', '0', 'changed', '2'), assigneeDraft('selected', '8', '李四', '2'),
    assigneeDraft('none', '0', '', '2'), assigneeDraft('unassigned', '0', '李四', '2', { group_id: '4' }),
    assigneeDraft('unassigned', '0', '李四', '2', { status: 'creating' })];
  for (const response of invalid) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? assigneeDraft() : response));
    await context.loadTaskDraft();
    fields.draftAssigneeSelect.value = '0';
    await assert.rejects(context.saveDraftAssignee(), /invalid responsible/);
    assert.equal(fields.draftAssigneeSelect.value, '0');
    await assert.rejects(context.saveDraftAssignee(), /load the current/);
  }
});

test('confirmation rejects a changed assignee or version and does not claim task success', async () => {
  for (const response of [assigneeDraft('selected', '9', '李四', '1', { status: 'succeeded', task_id: '123' }),
    assigneeDraft('selected', '8', '李四', '2', { status: 'succeeded', task_id: '123' })]) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? assigneeDraft('selected', '8') : response));
    await context.loadTaskDraft();
    await assert.rejects(context.confirmTaskDraft(), /invalid confirmation/);
    assert.doesNotMatch(fields.draftResult.textContent, /Task created:/);
  }
});

test('empty extracted name remains valid for none, legacy metadata, selected and unassigned drafts', async () => {
  for (const initialState of ['', 'none']) {
    for (const id of ['0', '8']) {
      const calls = [];
      const { context, fields } = draftEditPage(async (url, options) => {
        calls.push({ url, options });
        if (calls.length === 1) return reply(assigneeDraft(initialState, '0', ''));
        if (url.includes('/members?')) return memberPage([['8', 'Actual member']]);
        return reply(assigneeDraft(id === '0' ? 'unassigned' : 'selected', id, '', '2',
          url.endsWith('/confirm') ? { status: 'succeeded', task_id: '123' } : {}));
      });
      await context.loadTaskDraft();
      assert.equal(fields.btnConfirmDraft.disabled, false);
      if (id !== '0') await context.loadDraftMembers();
      fields.draftAssigneeSelect.value = id;
      await context.saveDraftAssignee();
      assert.match(fields.draftAssigneeSummary.textContent, /Original name: \(none\)/);
      assert.equal(fields.btnConfirmDraft.disabled, false);
      await context.confirmTaskDraft();
      assert.equal(JSON.parse(calls.at(-1).options.body).expected_assignee_id, id);
    }
  }
});

test('switching to another run through direct loading discards the previous directory choices', async () => {
  const { context, fields } = draftEditPage(async url => url.includes('/members?') ? memberPage([['8', 'Old member']]) :
    reply(assigneeDraft('none', '0', '', '1', { run_id: fields.draftRunID.value })));
  await context.loadTaskDraft();
  await context.loadDraftMembers();
  fields.draftRunID.value = '10';
  await context.loadTaskDraft();
  assert.deepEqual(fields.draftAssigneeSelect.options.map(item => item.value), ['', '0']);
  fields.draftAssigneeSelect.value = '8';
  await assert.rejects(context.saveDraftAssignee(), /choose Unassigned/);
});

test('a failed stale read cannot restore write access after the context changes back', async () => {
  let fail, calls = 0;
  const pending = new Promise((resolve, reject) => { fail = reject; });
  const { context, fields } = draftEditPage(async () => ++calls === 1 ? reply(assigneeDraft('none', '0', '')) : pending);
  await context.loadTaskDraft();
  const work = context.loadTaskDraft();
  fields.token.value = 'new';
  context.clearTaskDraftResult();
  fields.token.value = 'test-token';
  fail(new Error('stale read failed'));
  await work;
  assert.equal(fields.draftResult.textContent, '');
  await assert.rejects(context.saveDraftAssignee(), /load the current/);
});

test('reply retry preserves frozen named responsibility and rejects a changed saved identity', async () => {
  const initial = assigneeDraft('selected', '8', '李四', '2', { status: 'succeeded', task_id: '123',
    reply_status: 'pending', reply_msg_id: 'bot-task:9' });
  const changed = assigneeDraft('selected', '9', '李四', '2', { status: 'succeeded', task_id: '123',
    reply_status: 'accepted', reply_msg_id: 'bot-task:9' });
  let calls = 0;
  const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? initial : changed));
  await context.loadTaskDraft();
  await assert.rejects(context.retryTaskReply(), /invalid reply retry/);
  assert.match(fields.draftAssigneeSummary.textContent, /#8/);
  assert.equal(fields.btnRetryTaskReply.disabled, true);
});

test('draft writes preserve large revision strings and confirm the saved revision', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const result = draftData(calls.length === 1 ? 'Old title' : 'Revised title');
    result.data.draft.revision = calls.length === 1 ? '9007199254740993' : '9007199254740994';
    if (options.method === 'POST') {
      result.data.status = 'succeeded';
      result.data.task_id = '123';
    }
    return reply(result);
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'Revised title';
  await context.saveTaskDraft();
  assert.equal(JSON.parse(calls[1].options.body).expected_revision, '9007199254740993');
  await context.confirmTaskDraft();
  assert.equal(JSON.parse(calls[2].options.body).expected_revision, '9007199254740994');
  assert.match(fields.draftResult.textContent, /123/);
});

test('legacy draft responses remain readable but cannot save or confirm without a revision', async () => {
  let calls = 0;
  const result = draftData();
  delete result.data.draft.revision;
  const { context, fields } = draftEditPage(async () => { calls++; return reply(result); });
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'Old title');
  assert.equal(fields.btnSaveDraft.disabled, true);
  assert.equal(fields.btnConfirmDraft.disabled, true);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  await assert.rejects(context.confirmTaskDraft(), /load/);
  assert.equal(calls, 1);
});

test('draft revision conflict requires rereading even when the visible text is unchanged', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 2) return { ok: false, status: 409 };
    const result = draftData();
    result.data.draft.revision = calls.length === 1 ? '1' : '3';
    return reply(result);
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  await assert.rejects(context.saveTaskDraft(), /409/);
  assert.equal(fields.btnSaveDraft.disabled, true);
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'My title');
  await context.saveTaskDraft();
  assert.equal(JSON.parse(calls[3].options.body).expected_revision, '3');
});

test('draft editor loads text, saves exact old values and updates the next save baseline', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    const current = draftData('<script>title</script>', '', { run_id: '9007199254740997', team_id: '9007199254740993', group_id: '9007199254740995' });
    if (options.method === 'PUT') {
      const body = JSON.parse(options.body);
      current.data.draft.title = body.title;
      current.data.draft.description = body.description;
    }
    return reply(current);
  });
  fields.teamId.value = '9007199254740993';
  fields.toUserId.value = '9007199254740995';
  fields.draftRunID.value = '9007199254740997';
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, '<script>title</script>');
  assert.equal(fields.btnSaveDraft.disabled, false);
  fields.draftEditTitle.value = ' Revised title ';
  fields.draftEditDescription.value = ' Revised notes ';
  await context.saveTaskDraft();
  assert.equal(calls[1].url, '/api/v1/agent/runs/9007199254740997/draft');
  assert.equal(calls[1].options.method, 'PUT');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer test-token');
  assert.deepEqual(JSON.parse(calls[1].options.body), {
    title: 'Revised title', description: 'Revised notes', expected_title: '<script>title</script>', expected_description: '', expected_revision: '1'
  });
  assert.equal(fields.draftEditTitle.value, 'Revised title');
  assert.match(fields.draftResult.textContent, /Draft saved/);
  fields.draftEditDescription.value = '';
  await context.saveTaskDraft();
  assert.equal(JSON.parse(calls[2].options.body).expected_title, 'Revised title');
  assert.equal(JSON.parse(calls[2].options.body).expected_description, 'Revised notes');
  assert.equal(JSON.parse(calls[2].options.body).description, '');
  assert.equal(calls.length, 3);
});

test('draft conflict keeps proposed changes and requires a read before manual resubmission', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(draftData());
    if (calls.length === 2) return { ok: false, status: 409 };
    if (calls.length === 3) return reply(draftData('Other edit', 'Other notes'));
    const body = JSON.parse(options.body);
    return reply(draftData(body.title, body.description));
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  fields.draftEditDescription.value = 'My notes';
  await context.doSaveTaskDraft();
  assert.match(fields.draftResult.textContent, /HTTP 409/);
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.btnSaveDraft.disabled, true);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  assert.equal(calls.length, 2);
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.draftEditDescription.value, 'My notes');
  assert.match(fields.draftResult.textContent, /Other edit/);
  assert.equal(fields.btnSaveDraft.disabled, false);
  await context.saveTaskDraft();
  assert.deepEqual(JSON.parse(calls[3].options.body), {
    title: 'My title', description: 'My notes', expected_title: 'Other edit', expected_description: 'Other notes', expected_revision: '1'
  });
});

test('uncertain draft save preserves inputs and requires reading its final result', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    if (calls === 1) return reply(draftData());
    if (calls === 2) throw new Error('connection lost');
    return reply(draftData('My title', 'Old notes'));
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  await context.doSaveTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.btnSaveDraft.disabled, true);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.btnSaveDraft.disabled, false);
  assert.equal(calls, 3);
});

test('draft reload retains only edited fields and refreshes untouched description', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    return reply(calls === 1 ? draftData() : draftData('Other title', 'New server notes'));
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.draftEditDescription.value, 'New server notes');
});

test('draft save rejects unloaded, invalid and mismatched context before sending', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => { calls++; return reply(draftData()); });
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  await context.loadTaskDraft();
  fields.draftEditTitle.value = ' ';
  await assert.rejects(context.saveTaskDraft(), /1-200/);
  fields.draftEditTitle.value = 'New';
  fields.draftEditDescription.value = 'x'.repeat(2001);
  await assert.rejects(context.saveTaskDraft(), /2000/);
  fields.draftEditDescription.value = '';
  fields.draftRunID.value = '10';
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  fields.draftRunID.value = '9';
  fields.token.value = 'other-token';
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  assert.equal(calls, 1);
});

test('draft save suppresses duplicate submission and old response after selecting another group', async () => {
  let finish;
  const waiting = new Promise(resolve => { finish = resolve; });
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    return calls === 1 ? reply(draftData()) : waiting;
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  const save = context.saveTaskDraft();
  await context.saveTaskDraft();
  assert.equal(calls, 2);
  assert.equal(fields.btnSaveDraft.disabled, true);
  fields.teamGroupSelect.value = '4';
  context.selectTeamGroup();
  finish(reply(draftData('My title')));
  await save;
  assert.equal(fields.draftEditTitle.value, '');
  assert.equal(fields.draftEditTitle.disabled, true);
  assert.equal(fields.btnSaveDraft.disabled, true);
  assert.match(fields.draftResult.textContent, /Context changed/);
  assert.doesNotMatch(fields.draftResult.textContent, /My title/);
});

test('draft save keeps typing made while the submitted change is in flight', async () => {
  let finish;
  const waiting = new Promise(resolve => { finish = resolve; });
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(draftData());
    if (calls.length === 2) return waiting;
    return reply(draftData('Newer title', 'Old notes'));
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'Submitted title';
  const save = context.saveTaskDraft();
  fields.draftEditTitle.value = 'Newer title';
  finish(reply(draftData('Submitted title', 'Old notes')));
  await save;
  assert.equal(fields.draftEditTitle.value, 'Newer title');
  assert.match(fields.draftResult.textContent, /newer edits remain unsaved/);
  await context.saveTaskDraft();
  assert.equal(JSON.parse(calls[2].options.body).expected_title, 'Submitted title');
});

test('finished draft is readable but cannot be edited or saved', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => { calls++; return reply(draftData('Done', '', { status: 'succeeded', task_id: '123' })); });
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'Done');
  assert.equal(fields.draftEditTitle.disabled, true);
  assert.equal(fields.btnSaveDraft.disabled, true);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  assert.equal(calls, 1);
});

test('denied draft refresh preserves proposed text and disables further saves', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    return calls === 1 ? reply(draftData()) : { ok: false, status: 403 };
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'My title';
  await assert.rejects(context.loadTaskDraft(), /draft read HTTP 403/);
  assert.equal(fields.draftEditTitle.value, 'My title');
  assert.equal(fields.btnSaveDraft.disabled, true);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  assert.equal(calls, 2);
});

test('draft confirmation posts only saved text and displays an exact task ID', async () => {
  const calls = [];
  const largeRun = '9007199254740997';
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    return reply(draftData('<script>Saved task</script>', '', { run_id: largeRun,
      ...(options.method === 'POST' ? { status: 'succeeded', task_id: '9223372036854775806' } : {}) }));
  });
  fields.draftRunID.value = largeRun;
  fields.draftRequestKey.value = 'generation-only';
  await context.loadTaskDraft();
  assert.equal(fields.btnConfirmDraft.disabled, false);
  assert.equal(await context.confirmTaskDraft(), '9223372036854775806');
  assert.equal(calls[1].url, '/api/v1/agent/runs/' + largeRun + '/confirm');
  assert.equal(calls[1].options.method, 'POST');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer test-token');
  assert.equal(calls[1].options.headers['Idempotency-Key'], undefined);
  assert.deepEqual(JSON.parse(calls[1].options.body), { expected_title: '<script>Saved task</script>', expected_description: '', expected_revision: '1', expected_assignee_id: '0', expected_due_at_unix_ms: 0 });
  assert.match(fields.draftResult.textContent, /Task created: 9223372036854775806/);
  assert.match(fields.draftResult.textContent, /<script>Saved task<\/script>/);
  assert.equal(fields.btnSaveDraft.disabled, true);
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.equal(fields.draftEditTitle.disabled, true);
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  assert.equal(calls.length, 2);
});

test('draft confirmation requires saved unchanged text and is blocked while saving', async () => {
  const calls = [];
  let finishSave;
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (options.method === 'PUT') return new Promise(resolve => { finishSave = resolve; });
    return reply(draftData());
  });
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'Edited';
  context.refreshTaskDraftControls();
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.match(fields.draftConfirmHint.textContent, /Save your changes/);
  await assert.rejects(context.confirmTaskDraft(), /save your changes/);
  const saving = context.saveTaskDraft();
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  finishSave(reply(draftData('Edited')));
  await saving;
  assert.equal(fields.btnConfirmDraft.disabled, false);
  assert.equal(calls.length, 2);
  fields.token.value = 'other-user';
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  assert.equal(calls.length, 2);
});

test('draft confirmation suppresses double clicks and other draft operations in flight', async () => {
  let finish;
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (options.method === 'POST') return new Promise(resolve => { finish = resolve; });
    return reply(draftData());
  });
  await context.loadTaskDraft();
  const confirming = context.confirmTaskDraft();
  await context.confirmTaskDraft();
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.equal(fields.btnLoadDraft.disabled, true);
  assert.equal(fields.draftEditTitle.disabled, true);
  await assert.rejects(context.loadTaskDraft(), /already in progress/);
  await assert.rejects(context.prepareTaskDraft(), /already in progress/);
  await assert.rejects(context.saveTaskDraft(), /load the current waiting draft/);
  finish(reply(draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '123' })));
  await confirming;
  assert.equal(calls.length, 2);
});

test('uncertain confirmation requires a read and retries only the same frozen run', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(draftData());
    if (calls.length === 2) throw new Error('response lost');
    if (calls.length === 3) return reply(draftData('Old title', 'Old notes', { status: 'creating' }));
    return reply(draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '123' }));
  });
  await context.loadTaskDraft();
  await context.doConfirmTaskDraft();
  assert.match(fields.draftResult.textContent, /Load this same run/);
  assert.doesNotMatch(fields.draftResult.textContent, /Task created/);
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.equal(fields.btnSaveDraft.disabled, true);
  assert.equal(fields.draftEditTitle.disabled, true);
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  assert.equal(calls.length, 2);
  await context.loadTaskDraft();
  assert.equal(fields.btnConfirmDraft.disabled, false);
  assert.equal(fields.btnConfirmDraft.textContent, 'Retry confirmation');
  assert.match(fields.draftConfirmHint.textContent, /No automatic background retry/);
  assert.equal(fields.btnSaveDraft.disabled, true);
  assert.equal(await context.confirmTaskDraft(), '123');
  assert.equal(calls[1].url, calls[3].url);
  assert.equal(calls[1].options.body, calls[3].options.body);
  assert.equal(calls.length, 4);
});

test('confirmation response loss followed by succeeded read does not issue another create', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    if (calls === 1) return reply(draftData());
    if (calls === 2) return { ok: false, status: 504 };
    return reply(draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '9007199254740999' }));
  });
  await context.loadTaskDraft();
  await context.doConfirmTaskDraft();
  await context.loadTaskDraft();
  assert.match(fields.draftResult.textContent, /Task created: 9007199254740999/);
  assert.equal(fields.btnConfirmDraft.disabled, true);
  await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
  assert.equal(calls, 3);
});

test('confirmation conflict and permission failure do not report a failed or successful creation', async () => {
  for (const httpStatus of [409, 403, 503]) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => {
      calls++;
      return calls === 1 ? reply(draftData()) : { ok: false, status: httpStatus };
    });
    await context.loadTaskDraft();
    await context.doConfirmTaskDraft();
    assert.match(fields.draftResult.textContent, /creation result may be unrecorded/);
    assert.match(fields.draftResult.textContent, new RegExp('HTTP ' + httpStatus));
    assert.doesNotMatch(fields.draftResult.textContent, /Task created|no task created/);
    assert.equal(fields.btnConfirmDraft.disabled, true);
    await assert.rejects(context.confirmTaskDraft(), /load the current saved draft/);
    assert.equal(calls, 2);
  }
});

test('confirmed or creating reload replaces any old editable text with the frozen draft', async () => {
  let calls = 0;
  const { context, fields } = draftEditPage(async () => {
    calls++;
    return reply(calls === 1 ? draftData() : draftData('Frozen title', 'Frozen notes', { status: 'creating' }));
  });
  await context.loadTaskDraft();
  fields.draftEditTitle.value = 'Unsaved text';
  await context.loadTaskDraft();
  assert.equal(fields.draftEditTitle.value, 'Frozen title');
  assert.equal(fields.draftEditDescription.value, 'Frozen notes');
  assert.equal(fields.draftEditTitle.disabled, true);
  assert.equal(fields.btnConfirmDraft.disabled, false);
});

test('confirmation ignores success and error responses after a context change even when changed back', async () => {
  for (const responseFails of [false, true]) {
    let finish;
    let reject;
    const { context, fields } = draftEditPage(async (_url, options) => {
      if (options.method === 'POST') return new Promise((resolve, fail) => { finish = resolve; reject = fail; });
      return reply(draftData());
    });
    await context.loadTaskDraft();
    const confirming = context.confirmTaskDraft();
    fields.toUserId.value = '4';
    context.clearTaskDraftResult();
    fields.toUserId.value = '3';
    if (responseFails) reject(new Error('old transport error'));
    else finish(reply(draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '123' })));
    await confirming;
    assert.match(fields.draftResult.textContent, /Context changed/);
    assert.doesNotMatch(fields.draftResult.textContent, /Task created:|old transport error|Old title/);
    assert.equal(fields.btnConfirmDraft.disabled, true);
    assert.equal(fields.draftEditTitle.value, '');
  }
});

test('confirmation rejects malformed task IDs, non-success states and changed saved text', async () => {
  for (const bad of [
    draftData('Old title', 'Old notes', { status: 'succeeded', task_id: 9007199254740992 }),
    draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '0' }),
    draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '9223372036854775808' }),
    draftData('Old title', 'Old notes', { status: 'creating' }),
    draftData('Different title', 'Old notes', { status: 'succeeded', task_id: '123' }),
    draftData('Old title', 'Old notes', { status: 'succeeded', task_id: '123', run_id: '10' })
  ]) {
    let calls = 0;
    const { context, fields } = draftEditPage(async () => reply(++calls === 1 ? draftData() : bad));
    await context.loadTaskDraft();
    await context.doConfirmTaskDraft();
    assert.match(fields.draftResult.textContent, /invalid confirmation response/);
    assert.doesNotMatch(fields.draftResult.textContent, /Task created:/);
    assert.equal(fields.btnConfirmDraft.disabled, true);
  }
});

function taskReplyData(state = 'pending', overrides = {}) {
  return draftData('Saved task', '', { status: 'succeeded', task_id: '9007199254740999', reply_status: state,
    ...(['pending', 'accepted'].includes(state) ? { reply_msg_id: 'bot-task:9' } : {}), ...overrides });
}

test('pending confirmation requires a fresh read before replying without creating another task', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({ url, options });
    if (calls.length === 1) return reply(draftData('Saved task', ''));
    return reply(taskReplyData(url.endsWith('/reply/retry') ? 'accepted' : 'pending'));
  });
  await context.loadTaskDraft();
  assert.equal(await context.confirmTaskDraft(), '9007199254740999');
  assert.match(fields.draftResult.textContent, /Task created: 9007199254740999/);
  assert.match(fields.draftResult.textContent, /may already be accepted/);
  assert.equal(fields.btnRetryTaskReply.disabled, true);
  assert.match(fields.taskReplyHint.textContent, /Load this same run/);
  await assert.rejects(context.retryTaskReply(), /load this same successful run/);
  assert.equal(calls.length, 2);
  await context.loadTaskDraft();
  assert.equal(fields.btnRetryTaskReply.disabled, false);
  assert.equal(await context.retryTaskReply(), 'bot-task:9');
  assert.equal(calls[3].url, '/api/v1/agent/runs/9/reply/retry');
  assert.equal(calls[3].options.body, undefined);
  assert.equal(calls[3].options.headers.Authorization, 'Bearer test-token');
  assert.equal(calls[3].options.headers['Idempotency-Key'], undefined);
  assert.match(fields.taskReplyHint.textContent, /delivery to all members is not confirmed/);
  assert.equal(fields.btnRetryTaskReply.disabled, true);
  assert.equal(fields.btnConfirmDraft.disabled, true);
  assert.equal(calls.filter(c => c.url.endsWith('/confirm')).length, 1);
});

test('a saved task with no reply intent permits only a manual send after reading', async () => {
  const calls = [];
  const { context, fields } = draftEditPage(async (url, options) => {
    calls.push({url,options}); return reply(taskReplyData(calls.length === 1 ? 'not_started' : 'accepted'));
  });
  await context.loadTaskDraft();
  assert.equal(fields.btnRetryTaskReply.textContent, 'Send group reply');
  assert.equal(fields.btnRetryTaskReply.disabled, false);
  assert.equal(calls.length, 1);
  await context.retryTaskReply();
  assert.equal(calls.length, 2);
  assert.doesNotMatch(calls[1].url, /\/confirm$/);
});

test('disabled, unknown, accepted and legacy reply states never permit a retry', async () => {
  for (const state of ['disabled', 'unknown', 'accepted', undefined]) {
    let calls = 0;
    const data = taskReplyData(state);
    if (state === undefined) { delete data.data.reply_status; delete data.data.reply_msg_id; }
    const {context,fields} = draftEditPage(async () => {calls++;return reply(data);});
    await context.loadTaskDraft();
    assert.equal(fields.btnRetryTaskReply.disabled, true);
    await assert.rejects(context.retryTaskReply(), /load this same successful run/);
    assert.equal(calls,1);
    if (state === undefined) assert.match(fields.draftResult.textContent, /Reply status unavailable/);
    if (state === 'accepted') assert.match(fields.draftResult.textContent, /Delivery to all members is not confirmed/);
  }
});

test('uncertain reply retry keeps task success and blocks resending until a successful read', async () => {
  for (const failure of ['lost response',503,504,403]) {
    const calls = [];
    const {context,fields} = draftEditPage(async (url,options) => {
      calls.push({url,options});
      if (calls.length === 2) {if (failure === 'lost response') throw new Error('lost response');return {ok:false,status:failure};}
      return reply(taskReplyData(url.endsWith('/reply/retry') ? 'accepted' : 'pending'));
    });
    await context.loadTaskDraft();
    await context.doRetryTaskReply();
    assert.match(fields.draftResult.textContent,/Task created: 9007199254740999/);
    assert.match(fields.draftResult.textContent,/Load this same run/);
    assert.equal(fields.btnRetryTaskReply.disabled,true);
    await assert.rejects(context.retryTaskReply(),/load this same successful run/);
    assert.equal(calls.length,2);
    await context.loadTaskDraft();
    await context.retryTaskReply();
    assert.equal(calls.length,4);
    assert.equal(calls[1].url,calls[3].url);
    assert.equal(calls.filter(c=>c.url.endsWith('/confirm')).length,0);
  }
});

test('reply retry suppresses double clicks and excludes draft operations while in flight', async () => {
  let finish;const calls=[];
  const {context,fields}=draftEditPage(async(url,options)=>{
    calls.push({url,options});if(options.method==='POST')return new Promise(resolve=>{finish=resolve;});return reply(taskReplyData());
  });
  await context.loadTaskDraft();const pending=context.retryTaskReply();await context.retryTaskReply();
  for(const id of ['btnPrepareDraft','btnLoadDraft','btnSaveDraft','btnConfirmDraft','btnRetryTaskReply'])assert.equal(fields[id].disabled,true);
  await assert.rejects(context.loadTaskDraft(),/already in progress/);
  await assert.rejects(context.prepareTaskDraft(),/already in progress/);
  await assert.rejects(context.saveTaskDraft(),/load the current waiting draft/);
  await assert.rejects(context.confirmTaskDraft(),/load the current saved draft/);
  finish(reply(taskReplyData('accepted')));await pending;assert.equal(calls.length,2);
});

test('failed reply refresh keeps task ID and does not restore retry permission', async () => {
  let calls=0;
  const {context,fields}=draftEditPage(async()=>++calls===1?reply(taskReplyData()):{ok:false,status:403});
  await context.loadTaskDraft();await context.doLoadTaskDraft();
  assert.match(fields.draftResult.textContent,/Task created: 9007199254740999/);
  assert.equal(fields.btnRetryTaskReply.disabled,true);
  await assert.rejects(context.retryTaskReply(),/load this same successful run/);assert.equal(calls,2);
});

test('reply success or failure is ignored when the group changed even if it changed back', async () => {
  for(const fail of [false,true]){
    let finish, calls=0;
    const {context,fields}=draftEditPage(async()=>++calls===1?reply(taskReplyData()):new Promise(resolve=>{finish=resolve;}));
    await context.loadTaskDraft();const pending=context.retryTaskReply();
    fields.toUserId.value='4';context.clearTaskDraftResult();fields.toUserId.value='3';
    finish(fail?{ok:false,status:503}:reply(taskReplyData('accepted')));await pending;
    assert.match(fields.draftResult.textContent,/Context changed/);
    assert.doesNotMatch(fields.draftResult.textContent,/Group reply accepted/);
    assert.equal(fields.btnRetryTaskReply.disabled,true);
  }
});

test('reply retry rejects mismatched task IDs and message IDs without claiming acceptance', async () => {
  for(const bad of [taskReplyData('accepted',{task_id:'7'}),taskReplyData('accepted',{reply_msg_id:'bot-task:10'}),
      taskReplyData('pending'),taskReplyData('accepted',{draft:{title:'Changed',description:''}})]){
    let calls=0;const {context,fields}=draftEditPage(async()=>reply(++calls===1?taskReplyData():bad));
    await context.loadTaskDraft();await context.doRetryTaskReply();
    assert.match(fields.draftResult.textContent,/Task created: 9007199254740999/);
    assert.match(fields.draftResult.textContent,/invalid reply retry response/);
    assert.equal(fields.btnRetryTaskReply.disabled,true);
    assert.doesNotMatch(fields.draftResult.textContent,/Group reply accepted/);
  }
});

test('reply retry rejects token and run changes before sending', async () => {
  for(const changed of ['token','draftRunID']){
    let calls=0;const {context,fields}=draftEditPage(async()=>{calls++;return reply(taskReplyData());});
    await context.loadTaskDraft();fields[changed].value='different';
    await assert.rejects(context.retryTaskReply(),/load this same successful run/);assert.equal(calls,1);
  }
});

const triggerMessageID = '9007199254740993';
const triggerUserID = '9007199254740995';
function triggerMessage(overrides = {}) {
  return { id: triggerMessageID, msg_id: 'trigger-command', from_id: triggerUserID, sender_type: 1, initiator_id: '0',
    content_type: 1, content: '@AI 整理任务 明天跟进发布', chat_type: 2, to_id: '300', ...overrides };
}
function triggerStatus(status, overrides = {}) {
  return { message_id: triggerMessageID, team_id: '200', group_id: '300', status,
    run_id: status === 'completed' ? '9007199254741011' : '0', ...overrides };
}
async function triggerPage(fetch) {
  const value = page(fetch);
  value.fields.teamId.value = '200'; value.fields.toUserId.value = '300'; value.fields.chatType.value = '2';
  await value.context.refreshAgentTriggerIdentity();
  return value;
}
function triggerControls(entry) {
  const index = entry.children.findIndex(child => child.textContent === '查看 AI 草稿');
  return index < 0 ? null : { button: entry.children[index], status: entry.children[index + 1] };
}
function appendTrigger(context, message = triggerMessage(), scope = { token: 'test-token', teamID: '200', groupID: '300' }, history = false) {
  const entry = { children: [], appendChild(child) { this.children.push(child); } };
  context.attachAgentTriggerButton(entry, scope, message, history);
  return triggerControls(entry);
}

test('AI trigger history and realtime entries retain saved large IDs and show only valid own text commands', async () => {
  for (const origin of ['history', 'live']) {
    const { context, logEntries } = await triggerPage(async url => url.endsWith('/info') ? reply({ code: 0, data: { id: triggerUserID } }) :
      reply({ code: 0, data: { messages: [triggerMessage({ ...(origin === 'history' ? { chat_type: undefined, to_id: undefined } : {}) })], next_before_message_id: '0' } }));
    if (origin === 'history') await context.loadTeamGroupHistory();
    else { context.doConnect(); context.socket.onmessage({ data: JSON.stringify({ type: 'chat', data: triggerMessage() }) }); }
    assert.equal(logEntries.filter(triggerControls).length, 1, origin);
    assert.equal(context.isAgentTaskCommand('\u0085@AI\u0085整理任务\u0085处理😀\u0085'), true);
    assert.equal(context.isAgentTaskCommand('@AI 整理任务 ' + '😀'.repeat(2000)), true);
    assert.equal(context.isAgentTaskCommand('@AI 整理任务 ' + '😀'.repeat(2001)), false);
    for (const change of [{ from_id: '7' }, { sender_type: 2 }, { initiator_id: '7' }, { content_type: 4 }, { id: '0' }, { id: 9007199254740992 },
      { id: '' }, { chat_type: 1 }, { to_id: '4' }, { content: '@AI 普通问答' }, { content: '@AI 整理任务 ' },
      { content: '@AI整理任务 跟进' }, { content: '@AI 整理任务跟进' }, { content: '@AI 整理任务 \ud800' }, { content: '\ufeff@AI 整理任务 发布' }]) {
      assert.equal(appendTrigger(context, triggerMessage(change)), null, JSON.stringify(change));
    }
    assert.equal(appendTrigger(context, triggerMessage(), { token: 'old-token', teamID: '200', groupID: '300' }), null);
  }
});

test('trigger status has four safe states; only completed loads the exact run without any write', async () => {
  for (const status of ['queued', 'running', 'exhausted', 'completed']) {
    const calls = [], loads = [];
    const { context } = await triggerPage(async (url, options) => { calls.push([url, options]); return reply({ code: 0,
      data: url.endsWith('/info') ? { id: triggerUserID } : triggerStatus(status) }); });
    context.loadMultiDraftForTrigger = async (id, scope, current) => { assert.equal(current(), true); loads.push([id, { ...scope }]); };
    const control = appendTrigger(context); await control.button.onclick();
    assert.equal(calls[1][0], '/api/v1/teams/200/groups/300/agent-triggers/' + triggerMessageID);
    assert.equal(calls[1][1].headers.Authorization, 'Bearer test-token');
    assert.equal(calls[1][1].method, undefined); assert.equal(calls[1][1].body, undefined);
    assert.equal(control.button.disabled, false);
    assert.equal(loads.length, status === 'completed' ? 1 : 0);
    assert.match(control.status.textContent, status === 'completed' ? /逐项审查/ : status === 'exhausted' ? /人工排查/ : /稍后重新查看/);
    if (loads.length) { assert.equal(loads[0][0], '9007199254741011'); assert.deepEqual(loads[0][1], { token: 'test-token', teamID: '200', groupID: '300' }); }
  }
});

test('trigger status rejects malformed IDs, scope and state combinations without loading any run', async () => {
  const malformed = [null, [], triggerStatus('other'), triggerStatus('queued', { run_id: '1' }), triggerStatus('completed', { run_id: '0' }),
    triggerStatus('completed', { run_id: 9007199254741010 }), triggerStatus('queued', { message_id: '9007199254740994' }),
    triggerStatus('queued', { message_id: 9007199254740992 }), triggerStatus('queued', { team_id: '201' }), triggerStatus('queued', { group_id: '301' }),
    triggerStatus('queued', { lease_token: 'private-token' }), triggerStatus('completed', { run_id: '9223372036854775808' })];
  for (const data of malformed) {
    const { context } = await triggerPage(async url => reply({ code: 0, data: url.endsWith('/info') ? { id: triggerUserID } : data }));
    context.loadMultiDraftForTrigger = () => assert.fail('malformed success loaded run');
    const control = appendTrigger(context); await control.button.onclick();
    assert.match(control.status.textContent, /无法读取/); assert.doesNotMatch(control.status.textContent, /private-token/);
  }
});

test('trigger errors use fixed safe text and never expose response or fetch error bodies', async () => {
  for (const failure of ['http', 'code', 'network', 'panel']) {
    const { context } = await triggerPage(async url => {
      if (url.endsWith('/info')) return reply({ code: 0, data: { id: triggerUserID } });
      if (failure === 'network') throw new Error('Bearer test-token mysql private-password');
      if (failure === 'http') return { ok: false, status: 503, json: () => assert.fail('read error body') };
      return reply({ code: failure === 'code' ? 500 : 0, msg: 'private-password', data: triggerStatus('completed') });
    });
    context.loadMultiDraftForTrigger = () => { throw new Error('Bearer test-token private-panel'); };
    const control = appendTrigger(context); await control.button.onclick();
    assert.match(control.status.textContent, /无法读取/); assert.doesNotMatch(control.status.textContent, /test-token|private/);
  }
});

test('trigger clicks are mutually exclusive and stale token/group/page responses cannot load drafts', async () => {
  for (const field of ['token', 'toUserId', 'teamId', 'chatType', 'draftRunID']) {
    let finish, statusCalls = 0, loads = 0;
    const { context, fields } = await triggerPage(async url => {
      if (url.endsWith('/info')) return reply({ code: 0, data: { id: triggerUserID } });
      statusCalls++; return new Promise(resolve => { finish = resolve; });
    });
    context.loadMultiDraftForTrigger = async () => { loads++; };
    const control = appendTrigger(context), another = appendTrigger(context, triggerMessage({ id: '9007199254740997' }));
    const pending = control.button.onclick(); await control.button.onclick(); await another.button.onclick(); assert.equal(statusCalls, 1);
    const old = fields[field].value; fields[field].value = field === 'chatType' ? '1' : 'different'; context.clearTaskDraftResult();
    fields[field].value = old; context.clearTaskDraftResult();
    finish(reply({ code: 0, data: triggerStatus('completed') })); await pending;
    assert.equal(loads, 0, field); assert.equal(control.status.textContent, '', field);
  }
});

test('a panel review change during status lookup discards the response and preserves the current review', async () => {
  let finish, generation = 0, loads = 0;
  const { context } = await triggerPage(async url => url.endsWith('/info') ? reply({ code: 0, data: { id: triggerUserID } }) :
    new Promise(resolve => { finish = resolve; }));
  context.captureMultiDraftReviewState = () => { const saved = generation; return () => generation === saved; };
  context.loadMultiDraftForTrigger = async () => { loads++; };
  const control = appendTrigger(context), pending = control.button.onclick(); generation++;
  finish(reply({ code: 0, data: triggerStatus('completed') })); await pending;
  assert.equal(loads, 0); assert.equal(control.status.textContent, ''); assert.equal(control.button.disabled, false);
});

test('identity is read only for display and fails closed on malformed or switched-token profile replies', async () => {
  for (const id of [7, '0', '9223372036854775808', undefined]) {
    const { context, fields } = await triggerPage(async () => reply({ code: 0, data: { id } }));
    assert.equal(appendTrigger(context), null); assert.match(fields.agentTriggerIdentityHint.textContent, /读取失败/);
  }
  let finish;
  const { context, fields } = page(() => new Promise(resolve => { finish = resolve; }));
  fields.teamId.value = '200'; fields.toUserId.value = '300'; fields.chatType.value = '2';
  const pending = context.refreshAgentTriggerIdentity(); fields.token.value = 'other'; context.clearTaskDraftResult();
  fields.token.value = 'test-token'; context.clearTaskDraftResult(); finish(reply({ code: 0, data: { id: triggerUserID } })); await pending;
  assert.equal(appendTrigger(context), null);
});
