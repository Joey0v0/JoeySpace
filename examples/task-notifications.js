(() => {
  'use strict';
  const maxID = 9223372036854775807n;
  const validID = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= maxID;
  const sameScope = (a, b) => a === b || !!a && !!b && a.token === b.token && a.teamID === b.teamID;
  const emptyState = () => ({ items: [], cursor: '0', loaded: false, loading: false, error: '' });
  const validTime = value => Number.isSafeInteger(value) && value > 0 && value <= 253402300799999;

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
      this.state = emptyState();
    }

    get epoch() { return this._generation; }

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
      this.state = emptyState();
      this._notify();
    }

    reset() {
      this._scope = this._readScope();
      this._generation++;
      this.state = emptyState();
      this._notify();
    }

    _current(generation) {
      this.syncScope();
      return generation === this._generation;
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
        if (this._current(generation)) {
          this.state.loading = false;
          this._notify();
        }
      }
    }

    async _load(replace) {
      if (!this._scope) {
        this.state.error = '请先登录并填写有效的团队 ID';
        this._notify();
        return;
      }
      const scope = this._scope, generation = this._generation;
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
      } catch (_) {
        if (this._current(generation)) this.state.error = '通知读取失败，请重试';
      } finally {
        if (this._current(generation)) {
          this.state.loading = false;
          this._notify();
        }
      }
    }
  }

  globalThis.TaskNotificationsController = TaskNotificationsController;
})();
