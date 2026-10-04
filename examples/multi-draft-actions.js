(function () {
  'use strict';
  const Controller = globalThis.MultiDraftController;
  const draftFields = ['title', 'description', 'revision', 'assignee_id', 'assignee_name', 'assignee_resolution',
    'due_at_unix_ms', 'source_message_id'];

  function target(controller, index, statuses) {
    const item = controller.snapshot(index);
    if (!item || !Controller.validItem(item, controller.runID, index)) throw new Error('Load this item before continuing.');
    if (controller.reload.has(index)) throw new Error('Load this item again to review its latest state.');
    if (!statuses.includes(item.status)) throw new Error('This action is unavailable in the saved item state.');
    return JSON.parse(JSON.stringify(item));
  }

  function revision(draft, changed) {
    const value = changed ? String(BigInt(draft.revision) + 1n) : draft.revision;
    if (!validID(value)) throw new Error('This item version is exhausted.');
    return value;
  }

  function validText(value, minimum, maximum) {
    return typeof value === 'string' && Array.from(value).length >= minimum && Array.from(value).length <= maximum &&
      !Array.from(value).some(char => char.codePointAt(0) >= 0xd800 && char.codePointAt(0) <= 0xdfff);
  }

  function sameDraft(actual, original, changes = {}) {
    const expected = { ...original, ...changes };
    return draftFields.every(field => actual[field] === expected[field]) &&
      sameDeadlineMetadata(actual.deadline, { deadline: expected.deadline });
  }

  async function write(controller, index, suffix, init, matches, message, normalize) {
    const count = controller.items.length;
    const op = controller._begin();
    try {
      const data = await controller._request(op, '/api/v1/agent/runs/' + op.runID + '/drafts/' + index + suffix, init);
      if (!controller._current(op)) return;
      if (!data || data.run_id !== op.runID || data.team_id !== op.scope.teamID || data.group_id !== op.scope.groupID ||
          data.item_count !== count || !Controller.validItem(data.item, op.runID, index) || !matches(data.item)) {
        throw new Error('Invalid saved item response.');
      }
      if (normalize) normalize();
      if (!controller._current(op)) return;
      controller._replaceItem(data.item, { preserve: true, reviewed: false });
      controller.message = message;
      return data.item;
    } catch (error) {
      if (!controller._current(op)) return;
      controller._markReload(index, 'Load this item before continuing. Local input and any saved Task are retained.');
      throw error;
    } finally {
      controller._end(op);
    }
  }

  Controller.prototype.saveText = async function (index) {
    const item = target(this, index, ['waiting_confirmation']);
    const edit = this.edits.get(index);
    if (!edit || typeof edit.title !== 'string' || typeof edit.description !== 'string') throw new Error('Enter this item title and description.');
    const title = edit.title.trim(), description = edit.description.trim();
    const submittedTitle = edit.title, submittedDescription = edit.description;
    if (!validText(title, 1, 200) || !validText(description, 0, 2000)) throw new Error('Title or description is outside its allowed length.');
    const expectedRevision = revision(item.draft, title !== item.draft.title || description !== item.draft.description);
    return write(this, index, '', { method: 'PUT', body: JSON.stringify({ title, description, expected_revision: item.draft.revision }) },
      result => result.status === 'waiting_confirmation' && result.task_id === '0' &&
        sameDraft(result.draft, item.draft, { title, description, revision: expectedRevision }),
      'Item text saved. Review it before confirming.', () => {
        const current = this.edits.get(index), patch = {};
        if (current?.title === submittedTitle) patch.title = title;
        if (current?.description === submittedDescription) patch.description = description;
        if (Object.keys(patch).length) this.setEdit(index, patch);
      });
  };

  Controller.prototype.saveAssignee = async function (index) {
    const item = target(this, index, ['waiting_confirmation']);
    const choice = this.edits.get(index)?.assigneeID;
    if (!(choice === '0' || validID(choice) && this.membersLoaded && this.members.has(choice))) {
      throw new Error('Choose Unassigned or a member from the loaded team directory.');
    }
    const resolution = choice === '0' ? 'unassigned' : 'selected';
    const expectedRevision = revision(item.draft, choice !== item.draft.assignee_id || resolution !== item.draft.assignee_resolution);
    return write(this, index, '/assignee', { method: 'PUT', body: JSON.stringify({ assignee_id: choice, expected_revision: item.draft.revision }) },
      result => result.status === 'waiting_confirmation' && result.task_id === '0' &&
        sameDraft(result.draft, item.draft, { assignee_id: choice, assignee_resolution: resolution, revision: expectedRevision }),
      'Item responsible teammate saved. Review it before confirming.');
  };

  Controller.prototype.saveDeadline = async function (index) {
    const item = target(this, index, ['waiting_confirmation']);
    const input = this.edits.get(index)?.dueInput;
    if (typeof input !== 'string') throw new Error('Enter a complete Asia/Shanghai deadline, or explicitly leave it empty.');
    const savedInput = item.draft.due_at_unix_ms === 0 ? '' : shanghaiDeadlineInput(item.draft.due_at_unix_ms);
    const due = input === savedInput ? item.draft.due_at_unix_ms : parseShanghaiDeadline(input);
    if (!validDraftDue(due)) throw new Error('Invalid Asia/Shanghai deadline.');
    const resolution = due === 0 ? 'unset' : 'selected';
    const deadline = { ...item.draft.deadline, resolution };
    const expectedRevision = revision(item.draft, due !== item.draft.due_at_unix_ms || resolution !== item.draft.deadline.resolution);
    return write(this, index, '/deadline', { method: 'PUT', body: JSON.stringify({ due_at_unix_ms: due, expected_revision: item.draft.revision }) },
      result => result.status === 'waiting_confirmation' && result.task_id === '0' &&
        sameDraft(result.draft, item.draft, { due_at_unix_ms: due, deadline, revision: expectedRevision }),
      'Item deadline saved. Review the saved time before confirming.', () => {
        if (this.edits.get(index)?.dueInput === input) {
          this.setEdit(index, { dueInput: due === 0 ? '' : shanghaiDeadlineInput(due) });
        }
      });
  };

  Controller.prototype.confirm = async function (index) {
    const item = target(this, index, ['waiting_confirmation', 'creating']);
    if (this.dirty(index)) throw new Error('Save and review all local changes for this item before confirming.');
    if (['not_found', 'ambiguous', 'truncated'].includes(item.draft.assignee_resolution) || item.draft.deadline.resolution === 'needs_input') {
      throw new Error('Resolve and save this item responsible teammate and deadline before confirming.');
    }
    if (item.status === 'creating' && !this.reviewedReplies.has(index)) throw new Error('Load this creating item before explicitly retrying confirmation.');
    const d = item.draft;
    return write(this, index, '/confirm', { method: 'POST', body: JSON.stringify({ expected_title: d.title,
      expected_description: d.description, expected_revision: d.revision, expected_assignee_id: d.assignee_id,
      expected_due_at_unix_ms: d.due_at_unix_ms, expected_deadline_resolution: d.deadline.resolution }) },
      result => result.status === 'succeeded' && validID(result.task_id) && sameDraft(result.draft, d),
      'Task created for this item. Its group reply has a separate status; load this item before any reply retry.');
  };

  Controller.prototype.skip = async function (index) {
    const item = target(this, index, ['waiting_confirmation']);
    if (this.dirty(index)) throw new Error('Save and review local changes for this item before skipping.');
    return write(this, index, '/skip', { method: 'POST', body: JSON.stringify({ expected_revision: item.draft.revision }) },
      result => result.status === 'skipped' && result.task_id === '0' && result.reply_status === 'disabled' && result.reply_msg_id === '' &&
        sameDraft(result.draft, item.draft),
      'This candidate was skipped. No task was created for it.');
  };

  Controller.prototype.retryReply = async function (index) {
    const item = target(this, index, ['succeeded']);
    if (!this.reviewedReplies.has(index) || !['not_started', 'pending'].includes(item.reply_status)) {
      throw new Error('Load this successful item and review its pending or not-started reply before retrying.');
    }
    return write(this, index, '/reply/retry', { method: 'POST' },
      result => result.status === 'succeeded' && result.task_id === item.task_id && result.reply_status === 'accepted' &&
        result.reply_msg_id === 'bot-task:' + this.runID + (index === 0 ? '' : ':' + index) && sameDraft(result.draft, item.draft),
      'Original group reply accepted. Delivery to every member is not confirmed.');
  };
}());
