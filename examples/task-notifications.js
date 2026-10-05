(() => {
  'use strict';
  const maxID = 9223372036854775807n;
  const validID = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= maxID;
  const sameScope = (a, b) => a === b || !!a && !!b && a.token === b.token && a.teamID === b.teamID;
  const emptyState = () => ({ items: [], cursor: '0', loaded: false, loading: false, error: '' });
  const validTime = value => Number.isSafeInteger(value) && value > 0 && value <= 253402300799999;
  const exactKeys = (value, keys) => !!value && typeof value === 'object' && !Array.isArray(value) &&
    Reflect.ownKeys(value).length === keys.length && keys.every(key => Object.prototype.hasOwnProperty.call(value, key));

  function validatePage(body, before) {
    if (!body || body.code !== 0 || !body.data || !Array.isArray(body.data.notifications)) return null;
    const rows = body.data.notifications, cursor = body.data.next_before_notification_id;
    if (rows.length > 20 || !(cursor === '0' || validID(cursor))) return null;
    let previous = before === '0' ? maxID + 1n : BigInt(before);
    const items = [];
    for (const row of rows) {
      if (!row || !validID(row.notification_id) || !validID(row.task_id) || !validID(row.actor_id) ||
          BigInt(row.notification_id) >= previous || !Number.isInteger(row.from_status) || !Number.isInteger(row.to_status) ||
          row.from_status < 0 || row.from_status > 2 || row.to_status < 0 || row.to_status > 2 || row.from_status === row.to_status ||
          !validTime(row.created_at_unix_ms) || !(row.read_at_unix_ms === 0 ||
            validTime(row.read_at_unix_ms) && row.read_at_unix_ms >= row.created_at_unix_ms)) return null;
      previous = BigInt(row.notification_id);
      items.push({ notification_id: row.notification_id, task_id: row.task_id, actor_id: row.actor_id,
        from_status: row.from_status, to_status: row.to_status, created_at_unix_ms: row.created_at_unix_ms,
        read_at_unix_ms: row.read_at_unix_ms });
    }
    if (cursor !== '0' && (items.length !== 20 || cursor !== items[items.length - 1].notification_id)) return null;
    return { items, cursor };
  }

  class TaskNotificationsController {
    constructor({ fetch, getScope, onChange = () => {} }) {
      this._fetch = fetch;
      this._getScope = getScope;
      this._onChange = onChange;
      this._generation = 0;
      this._scope = this._readScope();
      this._clearRealtime();
      this.state = emptyState();
    }

    get epoch() { return this._generation; }
    get realtime() { return { hasUpdate: this._hasUpdate, recoveryPending: this._recoveryPending }; }

    _clearRealtime(blockHints = false) {
      this._hasUpdate = false;
      this._recoveryPending = false;
      this._hintSequence = 0;
      this._hintIDs = new Set();
      this._hintsBlocked = blockHints;
    }

    receiveHint(message, connectionToken) {
      this.syncScope();
      if (!this._scope || this._hintsBlocked || connectionToken !== this._scope.token ||
          !exactKeys(message, ['type', 'data']) || message.type !== 'task_notification_changed' ||
          !exactKeys(message.data, ['version', 'notification_id', 'team_id']) || message.data.version !== 1 ||
          !validID(message.data.notification_id) || !validID(message.data.team_id) || message.data.team_id !== this._scope.teamID) return false;
      const id = message.data.notification_id;
      if (this._hintIDs.has(id)) return true;
      this._hintIDs.add(id);
      if (this._hintIDs.size > 128) this._hintIDs.delete(this._hintIDs.values().next().value);
      if (this.state.items.some(item => item.notification_id === id)) return true;
      this._hintSequence++;
      const changed = !this._hasUpdate;
      this._hasUpdate = true;
      if (changed) this._notify();
      return true;
    }

    async recover(connectionToken) {
      this.syncScope();
      if (!this._scope || connectionToken !== this._scope.token) return;
      if (this.state.loading) {
        if (!this._recoveryPending) {
          this._recoveryPending = true;
          this._notify();
        }
        return;
      }
      return this._load(true);
    }

    _readScope() {
      try {
        const value = this._getScope();
        return value && typeof value.token === 'string' && value.token.trim() && validID(value.teamID)
          ? { token: value.token, teamID: value.teamID } : null;
      } catch (_) { return null; }
    }

    _notify() {
      try { this._onChange(this.state); } catch (_) { /* A view error must not expose a request or reject an event handler. */ }
    }

    syncScope() {
      const scope = this._readScope();
      if (sameScope(scope, this._scope)) return;
      this._scope = scope;
      this._generation++;
      this._clearRealtime();
      this.state = emptyState();
      this._notify();
    }

    reset() {
      this._scope = this._readScope();
      this._generation++;
      this._clearRealtime();
      this.state = emptyState();
      this._notify();
    }

    _current(generation) {
      this.syncScope();
      return generation === this._generation;
    }

    async _finish(generation) {
      if (!this._current(generation)) return;
      this.state.loading = false;
      if (this._recoveryPending) {
        this._recoveryPending = false;
        // The queued first-page read owns the next busy period before any callback.
        await this._load(true);
      } else this._notify();
    }

    async refresh() {
      this.syncScope();
      if (this.state.loading) return;
      return this._load(true);
    }

    async loadMore() {
      this.syncScope();
      if (this.state.loading || !this.state.loaded || this.state.cursor === '0') return;
      return this._load(false);
    }

    async markRead(notificationID) {
      this.syncScope();
      if (!this._scope || this.state.loading || !this.state.loaded || !validID(notificationID)) return;
      const item = this.state.items.find(row => row.notification_id === notificationID);
      if (!item || item.read_at_unix_ms !== 0) return;
      const scope = this._scope, generation = this._generation;
      this.state.loading = true;
      this.state.error = '';
      this._notify();
      try {
        if (!this._current(generation)) return;
        const response = await this._fetch('/api/v1/teams/' + scope.teamID + '/task-notifications/' + notificationID + '/read',
          { method: 'PUT', headers: { Authorization: 'Bearer ' + scope.token } });
        if (!this._current(generation)) return;
        if (response && (response.status === 401 || response.status === 403)) {
          this._clearRealtime(true);
          this.state = emptyState();
          this.state.error = response.status === 401 ? '登录已失效，请重新登录后刷新通知' : '当前无团队访问权限，请检查资格后刷新通知';
          return;
        }
        if (!response || !response.ok) throw new Error('request failed');
        const body = await response.json();
        if (!this._current(generation)) return;
        const result = body && body.code === 0 && body.data;
        if (!result || result.notification_id !== notificationID || !validTime(result.read_at_unix_ms) ||
            result.read_at_unix_ms < item.created_at_unix_ms) throw new Error('invalid response');
        this.state.items = this.state.items.map(row => row.notification_id === notificationID
          ? { ...row, read_at_unix_ms: result.read_at_unix_ms } : row);
      } catch (_) {
        if (this._current(generation)) this.state.error = '未能确认，请刷新或重试';
      } finally {
        await this._finish(generation);
      }
    }

    async _load(replace) {
      if (!this._scope) {
        this.state.error = '请先登录并填写有效的团队 ID';
        this._notify();
        return;
      }
      const scope = this._scope, generation = this._generation;
      const hintSequence = this._hintSequence;
      const before = replace ? '0' : this.state.cursor;
      if (replace) this.state = emptyState();
      this.state.loading = true;
      this.state.error = '';
      this._notify();
      try {
        if (!this._current(generation)) return;
        const response = await this._fetch('/api/v1/teams/' + scope.teamID +
          '/task-notifications?limit=20&before_notification_id=' + before,
          { method: 'GET', headers: { Authorization: 'Bearer ' + scope.token } });
        if (!this._current(generation)) return;
        if (response && (response.status === 401 || response.status === 403)) {
          this._clearRealtime(true);
          this.state = emptyState();
          this.state.error = response.status === 401 ? '登录已失效，请重新登录后刷新通知' : '当前无团队访问权限，请检查资格后刷新通知';
          return;
        }
        if (!response || !response.ok) throw new Error('request failed');
        const body = await response.json();
        if (!this._current(generation)) return;
        const page = validatePage(body, before);
        if (!page) throw new Error('invalid response');
        this.state.items = replace ? page.items : this.state.items.concat(page.items);
        this.state.cursor = page.cursor;
        this.state.loaded = true;
        this._hintsBlocked = false;
        if (replace && hintSequence === this._hintSequence) this._hasUpdate = false;
      } catch (_) {
        if (this._current(generation)) this.state.error = '通知读取失败，请重试';
      } finally {
        await this._finish(generation);
      }
    }
  }

  globalThis.TaskNotificationsController = TaskNotificationsController;
})();
