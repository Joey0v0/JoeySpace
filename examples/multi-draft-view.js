(() => {
  'use strict';
  const field = id => document.getElementById(id);
  const cards = new Map();
  let renderedRun = '', displayedRun = '', localMessage = '';
  let reviewEpoch = 0;
  const readScope = () => { try { return taskDraftScope(); } catch (_) { return null; } };
  const equalScope = (a, b) => a === b || !!a && !!b && a.token === b.token && a.teamID === b.teamID && a.groupID === b.groupID;
  let lastScope = readScope();
  const page = new globalThis.MultiDraftController({ fetch: (...args) => fetch(...args), getScope: taskDraftScope,
    isScopeCurrent: sameTaskDraftScope, onChange: () => { reviewEpoch++; render(); } });
  globalThis.multiDraftPage = page;

  function safeMessage(message, scope = page.scope) {
    let text = String(message || '');
    for (const token of [scope && scope.token, lastScope && lastScope.token, field('token').value]) {
      if (token) text = text.split(token).join('[redacted]');
    }
    return text.replace(/Bearer\s+\S+/gi, 'Bearer [redacted]');
  }

  function node(tag, text, parent) {
    const result = document.createElement(tag);
    if (text !== undefined) result.textContent = text;
    if (parent) parent.appendChild(result);
    return result;
  }

  function input(card, label, name, tag = 'input') {
    const row = node('div', undefined, card.root);
    row.className = 'config-row';
    const id = 'multiDraftItem' + card.index + name;
    const caption = node('label', label, row);
    caption.setAttribute('for', id);
    const control = node(tag, undefined, row);
    control.setAttribute('id', id);
    control.dataset.field = name;
    control.dataset.index = String(card.index);
    return control;
  }

  function button(card, title, action, disabled = true) {
    const control = node('button', title, card.root);
    control.type = 'button';
    control.dataset.action = action;
    control.dataset.index = String(card.index);
    control.disabled = disabled;
    control.onclick = () => { if (!control.disabled) return run(action, card.index); };
    return control;
  }

  function createCard(index) {
    const card = { index, root: node('article') };
    card.root.className = 'task-card';
    card.root.dataset.index = String(index);
    card.heading = node('h3', 'Candidate ' + (index + 1), card.root);
    card.state = node('p', '', card.root);
    card.saved = node('pre', '', card.root);
    card.saved.style.whiteSpace = 'pre-wrap';
    card.assignee = node('p', '', card.root);
    card.deadline = node('pre', '', card.root);
    card.deadline.style.whiteSpace = 'pre-wrap';
    card.title = input(card, 'Title:', 'title');
    card.title.type = 'text'; card.title.maxLength = 200;
    card.description = input(card, 'Description:', 'description', 'textarea');
    card.description.rows = 3; card.description.maxLength = 2000;
    card.saveText = button(card, 'Save title and description', 'saveText');
    card.assigneeID = input(card, 'Responsible teammate:', 'assigneeID', 'select');
    card.saveAssignee = button(card, 'Save responsible teammate', 'saveAssignee');
    card.dueInput = input(card, 'Due (Asia/Shanghai):', 'dueInput');
    card.dueInput.type = 'datetime-local'; card.dueInput.step = '0.001';
    node('small', 'An empty date is saved only when you explicitly click Save deadline. Original evidence is retained.', card.root);
    card.clearDue = button(card, 'Clear deadline input', 'clearDue');
    card.clearDue.onclick = () => {
      if (!card.clearDue.disabled) page.setEdit(index, { dueInput: '' });
    };
    card.saveDeadline = button(card, 'Save deadline', 'saveDeadline');
    card.hint = node('p', '', card.root);
    card.loadItem = button(card, 'Reload this item', 'loadItem');
    card.confirm = button(card, 'Confirm and create this task', 'confirm');
    card.skip = button(card, 'Skip this candidate', 'skip');
    card.retryReply = button(card, 'Retry this group reply', 'retryReply');
    for (const name of ['title', 'description', 'dueInput', 'assigneeID']) {
      card[name][name === 'assigneeID' ? 'onchange' : 'oninput'] = () => {
        if (!card[name].disabled) page.setEdit(index, { [name]: card[name].value });
      };
    }
    return card;
  }

  function evidence(draft) {
    const meta = draft.deadline;
    const time = value => deadlineEvidenceTime(value);
    return 'Saved deadline: ' + (draft.due_at_unix_ms === 0 ? 'Not set' : time(draft.due_at_unix_ms)) +
      '\nOriginal time text: ' + (meta.text || '(none)') + '\nSource: ' + meta.source +
      '\nSource message ID: ' + meta.source_message_id + '\nInterpretation reference: ' + time(meta.reference_unix_ms) +
      '\nTimezone: ' + meta.timezone + '\nResolution: ' + meta.resolution + '\nOriginal issue: ' + (meta.reason || '(none)') +
      '\nOriginal parsed candidate: ' + time(meta.parsed_unix_ms) +
      '\nFirst instruction reference (saved): ' + time(meta.instruction_reference_unix_ms);
  }

  function renderOptions(card, choice) {
    const options = [];
    const option = (value, text) => { const result = node('option', text); result.value = value; options.push(result); };
    option('', 'Choose a loaded teammate or explicitly leave unassigned');
    option('0', 'Unassigned');
    for (const [id, member] of page.members) option(id, (member.nickname || member.username) + ' #' + id);
    if (choice && choice !== '0' && !page.members.has(choice)) option(choice, 'Saved user #' + choice + ' (not in loaded directory)');
    const signature = JSON.stringify(options.map(option => [option.value, option.textContent]));
    if (signature !== card.optionsSignature) {
      card.assigneeID.replaceChildren(...options);
      card.optionsSignature = signature;
    }
    card.assigneeID.value = choice;
  }

  function renderCard(card, item, current) {
    const draft = item.draft, index = item.item_index;
    const edit = page.edits.get(index) || { title: draft.title, description: draft.description,
      assigneeID: ['not_found', 'ambiguous', 'truncated'].includes(draft.assignee_resolution) ? '' : draft.assignee_id,
      dueInput: draft.due_at_unix_ms ? shanghaiDeadlineInput(draft.due_at_unix_ms) : '' };
    const ready = current && !page.busy && !page.reload.has(index);
    const editable = ready && item.status === 'waiting_confirmation';
    const dirty = current && page.dirty(index);
    const resolved = !['not_found', 'ambiguous', 'truncated'].includes(draft.assignee_resolution) && draft.deadline.resolution !== 'needs_input';
    const member = page.members.get(draft.assignee_id);
    card.state.textContent = 'Task state: ' + item.status + '; Task ID: ' + item.task_id +
      '\nGroup reply: ' + item.reply_status + '; Message ID: ' + (item.reply_msg_id || '(none)') +
      (item.reply_status === 'accepted' ? '\nIM accepted this reply; delivery to group members is not confirmed.' : '') +
      (item.status === 'creating' ? '\nA creation attempt is saved. Reload and explicitly retry this item; no background execution is running.' : '');
    card.state.style.whiteSpace = 'pre-wrap';
    card.saved.textContent = 'Saved title: ' + draft.title + '\nSaved description: ' + (draft.description || '(none)') +
      '\nSaved revision: ' + draft.revision + '\nSource message ID: ' + draft.source_message_id;
    card.assignee.textContent = 'Original assignee name: ' + (draft.assignee_name || '(none)') +
      '\nAssignee resolution: ' + draft.assignee_resolution + '\nSaved responsible teammate: ' +
      (draft.assignee_id === '0' ? 'Unassigned' : (member ? (member.nickname || member.username) + ' ' : '') + '#' + draft.assignee_id);
    card.assignee.style.whiteSpace = 'pre-wrap';
    card.deadline.textContent = evidence(draft);
    for (const name of ['title', 'description', 'dueInput']) {
      if (card[name].value !== edit[name]) card[name].value = edit[name];
      card[name].disabled = !editable;
    }
    renderOptions(card, edit.assigneeID);
    card.assigneeID.disabled = !editable;
    card.clearDue.disabled = !editable;
    const unicode = value => typeof value === 'string' && !Array.from(value).some(char => char.codePointAt(0) >= 0xd800 && char.codePointAt(0) <= 0xdfff);
    const validText = unicode(edit.title) && edit.title.trim() && Array.from(edit.title.trim()).length <= 200 &&
      unicode(edit.description) && Array.from(edit.description.trim()).length <= 2000;
    let validDue = false, due = 0;
    try {
      const savedInput = draft.due_at_unix_ms ? shanghaiDeadlineInput(draft.due_at_unix_ms) : '';
      due = edit.dueInput === savedInput ? draft.due_at_unix_ms : parseShanghaiDeadline(edit.dueInput);
      validDue = validDraftDue(due) && !(card.dueInput.validity && card.dueInput.validity.badInput);
    } catch (_) {}
    const canChange = draft.revision !== '9223372036854775807';
    const textChanged = validText && (edit.title.trim() !== draft.title || edit.description.trim() !== draft.description);
    const assigneeChanged = edit.assigneeID !== draft.assignee_id || draft.assignee_resolution !== (edit.assigneeID === '0' ? 'unassigned' : 'selected');
    const deadlineChanged = due !== draft.due_at_unix_ms || draft.deadline.resolution !== (due === 0 ? 'unset' : 'selected');
    card.saveText.disabled = !editable || !validText || textChanged && !canChange;
    card.saveAssignee.disabled = !editable || !(edit.assigneeID === '0' || validID(edit.assigneeID) && page.membersLoaded && page.members.has(edit.assigneeID)) || assigneeChanged && !canChange;
    card.saveDeadline.disabled = !editable || !validDue || deadlineChanged && !canChange;
    card.loadItem.disabled = !current || page.busy;
    card.confirm.disabled = !ready || dirty || !resolved ||
      !(item.status === 'waiting_confirmation' || item.status === 'creating' && page.reviewedReplies.has(index));
    card.confirm.textContent = item.status === 'creating' ? 'Explicitly retry task creation' : 'Confirm and create this task';
    card.skip.disabled = !editable || dirty;
    card.retryReply.disabled = !ready || item.status !== 'succeeded' ||
      !['pending', 'not_started'].includes(item.reply_status) || !page.reviewedReplies.has(index);
    card.hint.textContent = !current ? 'Load this run in the current team group before acting.' : page.reload.has(index) ?
      'Reload this item to check the saved result. Your unsaved input is retained.' : dirty ? 'Save your changes before confirming or skipping.' :
      item.status === 'skipped' ? 'Skipped; no task or group reply was created for this candidate.' :
      !resolved ? 'Resolve the assignee or deadline explicitly and save before confirming. You may also skip this unchanged candidate.' :
      item.reply_status === 'unknown' ? 'The reply result is unknown. Reload before deciding whether to retry.' :
      item.status === 'succeeded' && !page.reviewedReplies.has(index) && ['pending', 'not_started'].includes(item.reply_status) ?
      'Reload this item to review its reply status before retrying.' : 'Review the saved values before acting.';
  }

  function summary(items) {
    if (!items.length) return 'Load or generate candidates in the current team group.';
    const counts = { waiting_confirmation: 0, creating: 0, succeeded: 0, skipped: 0 };
    const replies = { disabled: 0, not_started: 0, pending: 0, accepted: 0, unknown: 0 };
    for (const item of items) { counts[item.status]++; replies[item.reply_status]++; }
    const handled = counts.succeeded + counts.skipped === items.length;
    return (handled ? counts.skipped === items.length ? 'Candidates handled: all skipped; no tasks created.' :
      'Candidates handled: ' + counts.succeeded + ' tasks created, ' + counts.skipped + ' skipped.' : 'Candidates still need your review.') +
      '\nTasks — waiting: ' + counts.waiting_confirmation + ', creation result pending: ' + counts.creating +
      ', created: ' + counts.succeeded + ', skipped: ' + counts.skipped +
      '\nReplies — accepted by IM: ' + replies.accepted + ', pending: ' + replies.pending + ', unknown: ' + replies.unknown +
      ', not started: ' + replies.not_started + ', disabled: ' + replies.disabled +
      '\nReply acceptance does not confirm delivery. There is no automatic background retry.';
  }

  function render() {
    const scope = readScope();
    if (!equalScope(scope, lastScope)) {
      lastScope = scope;
      localMessage = '';
      page.invalidate();
      return;
    }
    if (displayedRun !== page.runID) { field('multiDraftRunID').value = page.runID; displayedRun = page.runID; }
    const same = page.scope && equalScope(scope, page.scope);
    const current = !!same && field('multiDraftRunID').value.trim() === page.runID;
    const items = same ? page.items : [];
    if (renderedRun !== page.runID || !items.length) { cards.clear(); field('multiDraftItems').replaceChildren(); renderedRun = page.runID; }
    for (const item of items) {
      if (!cards.has(item.item_index)) {
        const card = createCard(item.item_index); cards.set(item.item_index, card); field('multiDraftItems').appendChild(card.root);
      }
      renderCard(cards.get(item.item_index), item, current);
    }
    field('multiDraftSummary').textContent = summary(items) + (current && page.membersLoaded ?
      '\nLoaded team members: ' + page.members.size + (page.memberCursor === '0' ? ' (directory complete).' : ' (more pages available).') : '');
    field('multiDraftMessage').textContent = safeMessage(localMessage || page.message);
    field('multiDraftReference').textContent = page.reference ? 'Current generation request reference: ' + deadlineEvidenceTime(page.reference) +
      '\nCheck your device clock. This is this page\'s request reference; saved references are shown in each candidate.' :
      'Generation reference appears when you submit. Check your device clock; a refreshed page cannot recover an unknown request reference.';
    field('multiDraftRequestKey').value = page.requestKey;
    for (const id of ['multiDraftInstruction', 'multiDraftRequestKey', 'multiDraftRunID']) field(id).disabled = page.busy;
    field('btnMultiPrepare').disabled = page.busy || !scope;
    field('btnMultiLoad').disabled = page.busy || !scope || !validID(field('multiDraftRunID').value.trim());
    field('btnMultiNewKey').disabled = page.busy || !scope;
    const memberAllowed = current && items.some(item => item.status === 'waiting_confirmation');
    field('btnMultiMembers').disabled = page.busy || !memberAllowed;
    field('btnMultiMoreMembers').disabled = page.busy || !memberAllowed || !page.membersLoaded || page.memberCursor === '0';
  }

  async function run(action, ...args) {
    globalThis.invalidateMultiDraftPage();
    if (page.busy) return;
    const scope = readScope(), epoch = page.epoch;
    localMessage = '';
    try {
      if (typeof args[0] === 'number' && field('multiDraftRunID').value.trim() !== page.runID) throw new Error('Load this run before acting on a candidate.');
      await page[action](...args);
    } catch (error) {
      if (page.epoch === epoch && equalScope(readScope(), scope)) localMessage = safeMessage(error.message || 'Operation failed; reload to review the saved result.', scope);
    } finally {
      if (page.epoch === epoch && equalScope(readScope(), scope)) render();
    }
  }

  globalThis.invalidateMultiDraftPage = () => {
    const scope = readScope();
    if (!equalScope(scope, lastScope) || page.scope && !equalScope(scope, page.scope)) {
      lastScope = scope;
      localMessage = '';
      page.invalidate();
    } else render();
  };
  // A trigger-status read may only enter this panel if no intervening review
  // action changed it. The existing controller remains the sole draft state.
  globalThis.captureMultiDraftReviewState = () => {
    const epoch = reviewEpoch, scope = readScope();
    const fields = ['multiDraftRunID', 'multiDraftInstruction', 'multiDraftRequestKey'].map(id => field(id).value);
    return () => reviewEpoch === epoch && equalScope(readScope(), scope) &&
      fields.every((value, index) => field(['multiDraftRunID', 'multiDraftInstruction', 'multiDraftRequestKey'][index]).value === value);
  };
  globalThis.loadMultiDraftForTrigger = async (runID, scope, stillCurrent) => {
    if (!validID(runID) || !equalScope(readScope(), scope) || typeof stillCurrent !== 'function' || !stillCurrent()) throw new Error('Trigger context changed; view the current source again.');
    globalThis.invalidateMultiDraftPage();
    if (page.busy) throw new Error('Finish the current review operation before loading another source.');
    if (page.runID !== runID && page.items.some(item => page.dirty(item.item_index))) throw new Error('Save the current collection changes before loading another source.');
    localMessage = '';
    try { await page.load(runID); }
    finally { if (equalScope(readScope(), scope)) render(); }
  };
  field('btnMultiPrepare').onclick = () => run('prepare', field('multiDraftInstruction').value);
  field('btnMultiLoad').onclick = () => run('load', field('multiDraftRunID').value.trim());
  field('btnMultiNewKey').onclick = () => run('newRequestKey');
  field('btnMultiMembers').onclick = () => run('loadMembers', false);
  field('btnMultiMoreMembers').onclick = () => run('loadMembers', true);
  field('multiDraftRunID').oninput = () => { reviewEpoch++; render(); };
  field('multiDraftInstruction').oninput = () => { reviewEpoch++; };
  field('multiDraftRequestKey').oninput = () => { reviewEpoch++; if (!page.busy) { page.requestKey = field('multiDraftRequestKey').value; render(); } };
  render();
})();
