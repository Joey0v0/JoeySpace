(() => {
  'use strict';
  const clone = value => JSON.parse(JSON.stringify(value));
  const sameScope = (a, b) => !!a && !!b && a.token === b.token && a.teamID === b.teamID && a.groupID === b.groupID;
  const unicodeString = value => typeof value === 'string' && !Array.from(value).some(char => char.codePointAt(0) >= 0xd800 && char.codePointAt(0) <= 0xdfff);
  const text = (value, min, max) => typeof value === 'string' && value.trim() === value && Array.from(value).length >= min &&
    Array.from(value).length <= max && !Array.from(value).some(char => char.codePointAt(0) >= 0xd800 && char.codePointAt(0) <= 0xdfff);
  const nonnegativeID = value => value === '0' || validID(value);
  const keyValid = value => typeof value === 'string' && /^[A-Za-z0-9_.-]{1,64}$/.test(value);
  const defaultKey = () => 'collection-' + Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
  const defaults = item => ({ title: item.draft.title, description: item.draft.description,
    assigneeID: ['not_found', 'ambiguous', 'truncated'].includes(item.draft.assignee_resolution) ? '' : item.draft.assignee_id,
    dueInput: item.draft.due_at_unix_ms === 0 ? '' : shanghaiDeadlineInput(item.draft.due_at_unix_ms) });

  class MultiDraftController {
    constructor({ fetch, getScope, isScopeCurrent, onChange = () => {}, now = Date.now, randomKey = defaultKey }) {
      this.fetch = fetch; this.getScope = getScope; this.isScopeCurrent = isScopeCurrent;
      this.onChange = onChange; this.now = now; this.randomKey = randomKey;
      this.epoch = 0; this.busy = false; this.runID = ''; this.scope = null; this.items = [];
      this.edits = new Map(); this.reload = new Set(); this.reviewedReplies = new Set();
      this.members = new Map(); this.memberCursor = '0'; this.membersLoaded = false;
      this.requestKey = randomKey(); this.reference = 0; this.message = '';
      this._issuedKey = this.requestKey; this._generation = null; this._operation = null; this._nextOperation = 0;
    }

    static validItem(item, runID, index) {
      if (!validID(runID) || !Number.isInteger(index) || index < 0 || index > 4 || !item || typeof item !== 'object' || Array.isArray(item) ||
          item.item_index !== index || !nonnegativeID(item.task_id) || typeof item.reply_msg_id !== 'string') return false;
      if (!['waiting_confirmation', 'creating', 'succeeded', 'skipped'].includes(item.status) ||
          (item.status === 'succeeded' ? !validID(item.task_id) : item.task_id !== '0')) return false;
      const msgID = 'bot-task:' + runID + (index === 0 ? '' : ':' + index);
      if (item.status === 'skipped' && item.reply_status !== 'disabled') return false;
      switch (item.reply_status) {
        case 'disabled': case 'not_started': if (item.reply_msg_id !== '') return false; break;
        case 'pending': case 'accepted': if (item.status !== 'succeeded' || item.reply_msg_id !== msgID) return false; break;
        case 'unknown': if (item.status !== 'succeeded' || item.reply_msg_id !== '') return false; break;
        default: return false;
      }
      const d = item.draft;
      if (!d || typeof d !== 'object' || Array.isArray(d) || !text(d.title, 1, 200) || !text(d.description, 0, 2000) ||
          !validID(d.revision) || !nonnegativeID(d.source_message_id) || !nonnegativeID(d.assignee_id) ||
          !text(d.assignee_name, 0, 64) || !d.assignee_resolution || !validDraftAssignee(d) || !validDraftDue(d.due_at_unix_ms) ||
          !d.deadline || !validDeadlineMetadata(d.deadline, d.due_at_unix_ms)) return false;
      return !['creating', 'succeeded'].includes(item.status) ||
        !['not_found', 'ambiguous', 'truncated'].includes(d.assignee_resolution) && d.deadline.resolution !== 'needs_input';
    }

    static validCollection(data, runID, scope) {
      return !!data && typeof data === 'object' && !Array.isArray(data) && validID(runID) && data.run_id === runID &&
        !!scope && data.team_id === scope.teamID && data.group_id === scope.groupID && validID(data.team_id) && validID(data.group_id) &&
        Number.isInteger(data.item_count) && data.item_count >= 1 && data.item_count <= 5 && Array.isArray(data.items) &&
        data.items.length === data.item_count && Array.from(data.items, (item, index) => MultiDraftController.validItem(item, runID, index)).every(Boolean);
    }

    _notify() { this.onChange(this); }

    invalidate() {
      this.epoch++; this.busy = false; this._operation = null; this.runID = ''; this.scope = null;
      this.items = []; this.edits.clear(); this.reload.clear(); this.reviewedReplies.clear(); this.members.clear();
      this.memberCursor = '0'; this.membersLoaded = false; this.message = ''; this._notify();
    }

    newRequestKey() {
      if (this.busy) throw new Error('another operation is in progress');
      const key = this.randomKey();
      if (!keyValid(key) || key === this._issuedKey) throw new Error('a fresh valid request key is required');
      this.invalidate(); this.requestKey = key; this._issuedKey = key; this.reference = 0; this._generation = null; this._notify();
      return key;
    }

    _begin(runID = this.runID) {
      if (this.busy) throw new Error('another operation is in progress');
      const scope = this.getScope();
      if (!scope || typeof scope.token !== 'string' || !scope.token || !validID(scope.teamID) || !validID(scope.groupID) || !this.isScopeCurrent(scope)) {
        throw new Error('select a current team group and login');
      }
      if (this.scope && !sameScope(this.scope, scope)) throw new Error('context changed; reload the current group');
      if (runID !== this.runID || runID !== '' && !validID(runID)) throw new Error('load the current run before operating');
      this.scope = { ...scope };
      const op = { epoch: this.epoch, scope: { ...scope }, runID, id: ++this._nextOperation };
      this.busy = true; this._operation = op; this._notify();
      return op;
    }

    _current(op) {
      return !!op && this._operation?.id === op.id && this.epoch === op.epoch && this.runID === op.runID &&
        sameScope(this.scope, op.scope) && this.isScopeCurrent(op.scope);
    }

    _end(op) {
      if (this._operation?.id !== op.id) return;
      this.busy = false; this._operation = null; this._notify();
    }

    async _request(op, path, init = {}) {
      if (!this._current(op)) throw new Error('context changed; request discarded');
      const headers = { ...(init.headers || {}), Authorization: 'Bearer ' + op.scope.token };
      if (init.body !== undefined) headers['Content-Type'] = 'application/json';
      const response = await this.fetch(path, { ...init, headers });
      if (!this._current(op)) throw new Error('context changed; response discarded');
      if (!response.ok) throw new Error('request HTTP ' + response.status);
      const result = await response.json();
      if (!this._current(op)) throw new Error('context changed; response discarded');
      if (!result || result.code !== 0 || !result.data || typeof result.data !== 'object' || Array.isArray(result.data)) throw new Error('invalid response');
      return result.data;
    }

    snapshot(index) {
      if (!this.scope || !this.isScopeCurrent(this.scope) || !validID(this.runID) || !Number.isInteger(index) || !this.items[index] ||
          !MultiDraftController.validItem(this.items[index], this.runID, index)) throw new Error('load the current item before operating');
      return this.items[index];
    }

    _replaceItem(item, { preserve = true, reviewed = false } = {}) {
      const index = item.item_index;
      if (!MultiDraftController.validItem(item, this.runID, index) || index >= this.items.length) throw new Error('invalid item response');
      const old = this.items[index], baseline = old && defaults(old), prior = this.edits.get(index);
      const input = defaults(item);
      if (preserve && old?.status === 'waiting_confirmation' && item.status === 'waiting_confirmation' && prior) {
        for (const field of Object.keys(input)) if (prior[field] !== baseline[field]) input[field] = prior[field];
      }
      this.items[index] = clone(item);
      if (item.status === 'waiting_confirmation') this.edits.set(index, input); else this.edits.delete(index);
      this.reload.delete(index); this.reviewedReplies.delete(index);
      if (reviewed) this.reviewedReplies.add(index);
    }

    _markReload(index, message) {
      this.reload.add(index); this.reviewedReplies.delete(index); this.message = message; this._notify();
    }

    _acceptCollection(data, op) {
      if (!MultiDraftController.validCollection(data, op.runID, op.scope) || this.items.length && this.items.length !== data.item_count) throw new Error('invalid collection response');
      if (!this.items.length) this.items = Array(data.item_count).fill(null);
      for (const item of data.items) this._replaceItem(item, { reviewed: true });
      this.message = 'Collection loaded; review each item independently.';
    }

    async prepare(instruction) {
      if (typeof instruction !== 'string' || !text(instruction.trim(), 1, 2000)) throw new Error('enter an instruction of 1 to 2000 characters');
      instruction = instruction.trim();
      if (!keyValid(this.requestKey) || this.requestKey !== this._issuedKey) throw new Error('unknown request key; explicitly choose a new key');
      const scope = this.getScope();
      if (this._generation && (!sameScope(this._generation.scope, scope) || this._generation.instruction !== instruction)) throw new Error('request key already bound; explicitly choose a new key');
      const op = this._begin();
      try {
        if (!this._generation) {
          const reference = this.now();
          if (!validDraftDue(reference) || reference === 0) throw new Error('invalid instruction reference');
          this.reference = reference;
          this._generation = { instruction, reference, scope: { ...op.scope } };
        }
        const data = await this._request(op, '/api/v1/teams/' + op.scope.teamID + '/groups/' + op.scope.groupID + '/task-draft-collections', {
          method: 'POST', headers: { 'Idempotency-Key': this.requestKey },
          body: JSON.stringify({ instruction: this._generation.instruction, instruction_reference_unix_ms: this._generation.reference })
        });
        if (!validID(data.run_id)) throw new Error('invalid generated run');
        if (this._generation.runID && this._generation.runID !== data.run_id) throw new Error('request key returned a different run; keep the known result and reload');
        this._generation.runID = data.run_id;
        if (this.runID !== data.run_id) {
          this.items = []; this.edits.clear(); this.reload.clear(); this.reviewedReplies.clear();
          this.members.clear(); this.membersLoaded = false; this.memberCursor = '0';
          this.runID = data.run_id; op.runID = data.run_id;
        }
        this._acceptCollection(await this._request(op, '/api/v1/agent/runs/' + op.runID + '/drafts'), op);
        return this.items;
      } catch (error) {
        if (this._current(op)) {
          for (let index = 0; index < this.items.length; index++) this.reload.add(index);
          this.reviewedReplies.clear(); this.message = error.message + '; keep the same key for an unchanged retry, or explicitly start a new key.';
        }
        throw error;
      } finally { this._end(op); }
    }

    async load(runID) {
      if (!validID(runID)) throw new Error('enter a valid run ID');
      if (this.busy) throw new Error('another operation is in progress');
      const scope = this.getScope();
      if (this.runID !== runID || this.scope && !sameScope(this.scope, scope)) this.invalidate();
      this.runID = runID;
      const op = this._begin();
      try {
        this._acceptCollection(await this._request(op, '/api/v1/agent/runs/' + runID + '/drafts'), op);
        return this.items;
      } catch (error) {
        if (this._current(op)) {
          for (let index = 0; index < this.items.length; index++) this.reload.add(index);
          this.reviewedReplies.clear(); this.message = error.message + '; reload before further writes.';
        }
        throw error;
      } finally { this._end(op); }
    }

    async loadItem(index) {
      this.snapshot(index);
      const op = this._begin();
      try {
        const data = await this._request(op, '/api/v1/agent/runs/' + op.runID + '/drafts/' + index);
        if (data.run_id !== op.runID || data.team_id !== op.scope.teamID || data.group_id !== op.scope.groupID || data.item_count !== this.items.length ||
            !MultiDraftController.validItem(data.item, op.runID, index)) throw new Error('invalid item response');
        this._replaceItem(data.item, { reviewed: true }); this.message = 'Item reloaded.'; return this.items[index];
      } catch (error) {
        if (this._current(op)) this._markReload(index, error.message + '; reload this item before further writes.');
        throw error;
      } finally { this._end(op); }
    }

    setEdit(index, patch) {
      const item = this.snapshot(index);
      if (item.status !== 'waiting_confirmation' || !patch || typeof patch !== 'object' || Array.isArray(patch) ||
          Object.entries(patch).some(([field, value]) => !['title', 'description', 'assigneeID', 'dueInput'].includes(field) || typeof value !== 'string')) throw new Error('only waiting item inputs can be edited');
      this.edits.set(index, { ...defaults(item), ...this.edits.get(index), ...patch }); this._notify();
    }

    dirty(index) {
      const item = this.snapshot(index), baseline = defaults(item), input = this.edits.get(index);
      return !!input && Object.keys(baseline).some(field => input[field] !== baseline[field]);
    }

    async loadMembers(more = false) {
      if (!this.items.length) throw new Error('load a collection before its member directory');
      if (more && (!this.membersLoaded || this.memberCursor === '0')) throw new Error('no more member page is available');
      const op = this._begin(), cursor = more ? this.memberCursor : '0';
      try {
        const data = await this._request(op, '/api/v1/teams/' + op.scope.teamID + '/members?after_user_id=' + cursor + '&limit=100');
        if (!Array.isArray(data.members) || data.members.length > 100 || !nonnegativeID(data.next_after_user_id)) throw new Error('invalid member page');
        let previous = BigInt(cursor);
        const members = new Map(more ? this.members : []);
        for (const member of data.members) {
          if (!member || !validID(member.user_id) || BigInt(member.user_id) <= previous || typeof member.username !== 'string' || !member.username.trim() ||
              !unicodeString(member.username) || !unicodeString(member.nickname)) throw new Error('invalid member entry');
          previous = BigInt(member.user_id); members.set(member.user_id, clone(member));
        }
        if (data.next_after_user_id !== '0' && (!data.members.length || data.next_after_user_id !== data.members.at(-1).user_id)) throw new Error('invalid member cursor');
        this.members = members; this.memberCursor = data.next_after_user_id; this.membersLoaded = true;
        this.message = this.memberCursor === '0' ? 'All member pages loaded; membership is checked again when saving.' : 'More members exist; explicitly load the next page.';
        return this.members;
      } catch (error) {
        if (this._current(op)) this.message = error.message + '; existing member choices remain available.';
        throw error;
      } finally { this._end(op); }
    }
  }
  globalThis.MultiDraftController = MultiDraftController;
})();
