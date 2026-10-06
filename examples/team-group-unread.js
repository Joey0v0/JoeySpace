(() => {
  'use strict';
  const maxID = 9223372036854775807n;
  const validID = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= maxID;
  const validCount = value => value === '0' || validID(value);
  const sameScope = (a, b) => a === b || !!a && !!b && a.token === b.token && a.teamID === b.teamID && a.groupID === b.groupID;
  const exactKeys = (value, keys) => !!value && typeof value === 'object' && !Array.isArray(value) &&
    Reflect.ownKeys(value).length === keys.length && keys.every(key => Object.prototype.hasOwnProperty.call(value, key));
  const sortIDs = ids => [...new Set(ids)].sort((a, b) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0);
  const readFailed = '未读数读取失败，请显式刷新重试。';
  const markUncertain = '确认结果尚未确认，请刷新未读数或显式重试本页确认。';

  function createTeamGroupUnreadPage({ document, fetch }) {
    const field = id => document.getElementById(id);
    const countNode = field('groupUnreadCount'), statusNode = field('groupUnreadStatus');
    const refreshButton = field('btnGroupUnreadRefresh'), readButton = field('btnGroupReadLoaded');
    let epoch = 0, scope = null, pendingIDs = [], count = null, busy = false, blocked = false, message = '';

    function readScope() {
      const token = field('token').value;
      const teamID = field('teamId').value.trim(), groupID = field('toUserId').value.trim();
      return typeof token === 'string' && token.trim() && validID(teamID) && validID(groupID) && field('chatType').value === '2'
        ? { token, teamID, groupID } : null;
    }

    function render() {
      countNode.textContent = count === null ? '尚未查询' : count;
      statusNode.textContent = message || (scope
        ? '请手动刷新未读数；读取历史或收到消息不会自动已读。'
        : '请输入 Token、有效团队和群 ID，并选择群聊。');
      refreshButton.disabled = busy || blocked || !scope;
      readButton.disabled = busy || blocked || !scope || pendingIDs.length === 0;
    }

    function invalidate() {
      epoch++;
      scope = readScope();
      pendingIDs = [];
      count = null;
      busy = false;
      blocked = false;
      message = '';
      render();
    }

    function syncScope() {
      if (!sameScope(scope, readScope())) invalidate();
    }

    function current(generation) {
      syncScope();
      return epoch === generation;
    }

    function loaded(loadedScope, ids) {
      syncScope();
      if (!scope || blocked || !sameScope(scope, loadedScope)) return false;
      // A newer history page supersedes an in-flight confirmation even when its
      // scope and IDs are identical. Its response cannot clear this new target.
      epoch++;
      busy = false;
      count = null;
      if (!Array.isArray(ids) || ids.length > 20 || !ids.every(validID)) {
        pendingIDs = [];
        message = '本次历史页的消息 ID 无效，请重新读取历史。';
        render();
        return false;
      }
      pendingIDs = sortIDs(ids);
      message = pendingIDs.length ? '本次历史页已加载，可显式确认这些消息为已读。' : '本次历史页没有可确认的消息。';
      render();
      return true;
    }

    function validate(body, requestScope, ids) {
      const keys = ids ? ['team_id', 'group_id', 'unread_count', 'message_ids'] : ['team_id', 'group_id', 'unread_count'];
      if (!body || body.code !== 0 || !exactKeys(body.data, keys)) return null;
      const data = body.data;
      if (data.team_id !== requestScope.teamID || data.group_id !== requestScope.groupID || !validCount(data.unread_count)) return null;
      if (ids) {
        const echoed = data.message_ids;
        if (!Array.isArray(echoed) || echoed.length !== ids.length || !echoed.every(validID) || new Set(echoed).size !== ids.length) return null;
        const sorted = sortIDs(echoed);
        if (!sorted.every((id, index) => id === ids[index])) return null;
      }
      return data.unread_count;
    }

    async function request(mark) {
      syncScope();
      if (!scope || busy || blocked || mark && pendingIDs.length === 0) return;
      const generation = epoch, requestScope = { ...scope }, ids = mark ? [...pendingIDs] : null;
      busy = true;
      count = null;
      message = mark ? '正在确认本次已加载消息…' : '正在查询未读数…';
      render();
      try {
        const headers = { Authorization: 'Bearer ' + requestScope.token };
        const init = { method: mark ? 'POST' : 'GET', headers };
        if (mark) {
          headers['Content-Type'] = 'application/json';
          init.body = JSON.stringify({ message_ids: ids });
        }
        const response = await fetch('/api/v1/teams/' + requestScope.teamID + '/groups/' + requestScope.groupID + (mark ? '/read' : '/unread'), init);
        if (!current(generation)) return;
        if (response && (response.status === 401 || response.status === 403)) {
          epoch++;
          pendingIDs = [];
          count = null;
          busy = false;
          blocked = true;
          message = '当前身份或群权限已失效，请重新登录或修改范围。';
          render();
          return;
        }
        if (!response || !response.ok || response.status !== 200) throw new Error();
        const body = await response.json();
        if (!current(generation)) return;
        const nextCount = validate(body, requestScope, ids);
        if (nextCount === null) throw new Error();
        count = nextCount;
        if (mark) pendingIDs = [];
        message = mark ? '本次已加载消息已确认；未读数包含当前群的其他消息。'
          : pendingIDs.length ? '未读数已刷新；本次历史页仍需显式确认。' : '未读数已刷新；可加载需要确认的历史页。';
      } catch (_) {
        if (!current(generation)) return;
        count = null;
        message = mark ? markUncertain : readFailed;
      } finally {
        if (current(generation)) {
          busy = false;
          render();
        }
      }
    }

    refreshButton.onclick = () => request(false);
    readButton.onclick = () => request(true);
    for (const id of ['token', 'teamId', 'toUserId', 'chatType']) {
      field(id).addEventListener('input', invalidate);
      field(id).addEventListener('change', invalidate);
    }
    field('teamGroupSelect').addEventListener('change', invalidate);
    invalidate();
    return Object.freeze({ loaded, invalidate });
  }

  globalThis.createTeamGroupUnreadPage = createTeamGroupUnreadPage;
  if (typeof document !== 'undefined') {
    globalThis.teamGroupUnreadPage = createTeamGroupUnreadPage({ document, fetch: (...args) => globalThis.fetch(...args) });
  }
})();
