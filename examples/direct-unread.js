(() => {
  'use strict';

  const maxID = 9223372036854775807n;
  const validID = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= maxID;
  const validCount = value => value === '0' || validID(value);
  const sameScope = (a, b) => a === b || !!a && !!b && a.token === b.token && a.peerID === b.peerID;
  const sortIDs = ids => [...new Set(ids)].sort((a, b) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0);

  function createDirectUnreadPage({ document, fetch }) {
    const field = id => document.getElementById(id);
    const list = field('directHistoryList'), status = field('directUnreadStatus'), countNode = field('directUnreadCount');
    const latestButton = field('btnDirectLatest'), restartButton = field('btnDirectRestart'), olderButton = field('btnDirectOlder');
    const refreshButton = field('btnDirectUnreadRefresh'), readButton = field('btnDirectReadLoaded');
    let epoch = 0, scope = null, cursor = '', finished = false, pendingIDs = [], count = null;
    let busy = false, blocked = false, message = '';

    function readScope() {
      const token = field('token').value, peerID = field('toUserId').value.trim();
      return typeof token === 'string' && token.trim() && validID(peerID) && field('chatType').value === '1'
        ? { token, peerID } : null;
    }

    function render() {
      countNode.textContent = count === null ? '尚未查询' : count;
      status.textContent = message || (scope ? '请手动加载历史和刷新未读数；读取或收到消息不会自动已读。'
        : '请输入 Token、有效接收人 ID，并选择单聊类型 1。');
      latestButton.disabled = busy || blocked || !scope;
      restartButton.disabled = busy || blocked || !scope || (!cursor && !finished);
      olderButton.disabled = busy || blocked || !scope || !cursor || finished;
      refreshButton.disabled = busy || blocked || !scope;
      readButton.disabled = busy || blocked || !scope || pendingIDs.length === 0;
    }

    function invalidate() {
      epoch++;
      scope = readScope();
      cursor = '';
      finished = false;
      pendingIDs = [];
      count = null;
      busy = false;
      blocked = false;
      message = '';
      list.replaceChildren();
      render();
    }

    function syncScope() {
      if (!sameScope(scope, readScope())) invalidate();
    }

    function current(generation) {
      syncScope();
      return generation === epoch;
    }

    function deny() {
      epoch++;
      pendingIDs = [];
      cursor = '';
      finished = false;
      count = null;
      busy = false;
      blocked = true;
      list.replaceChildren();
      message = '登录身份或单聊访问已失效，请重新登录或修改范围。';
      render();
    }

    function validHistory(body, requestScope) {
      const data = body && body.data;
      if (!body || body.code !== 0 || !data || !Array.isArray(data.messages) || data.messages.length > 20 ||
          !(data.next_before_message_id === '0' || validID(data.next_before_message_id))) return null;
      let previous = null;
      const received = [];
      for (const item of data.messages) {
        if (!item || !validID(item.id) || !validID(item.from_id) || !validID(item.to_id) ||
            (item.from_id !== requestScope.peerID && item.to_id !== requestScope.peerID) ||
            typeof item.msg_id !== 'string' || !item.msg_id || typeof item.content !== 'string' ||
            !Number.isInteger(item.content_type) || !Number.isInteger(item.created_at_unix_ms) ||
            previous !== null && BigInt(item.id) >= BigInt(previous)) return null;
        if (item.from_id === requestScope.peerID && item.to_id !== requestScope.peerID) received.push(item.id);
        previous = item.id;
      }
      if (data.next_before_message_id !== '0' &&
          (!previous || data.next_before_message_id !== previous)) return null;
      return { messages: data.messages, next: data.next_before_message_id, received: sortIDs(received) };
    }

    function showHistory(messages, mode) {
      const displayed = new Set(Array.from(list.children, row => row.dataset.messageId));
      const rows = [];
      for (const item of messages) {
        if (mode === 'latest' && displayed.has(item.id)) continue;
        const row = document.createElement('p');
        row.dataset.messageId = item.id;
        row.textContent = '#' + item.id + ' ' + item.from_id + ' → ' + item.to_id + ': ' + item.content;
        rows.push(row);
      }
      if (mode === 'latest') list.prepend(...rows);
      else if (mode === 'older') rows.forEach(row => list.appendChild(row));
      else list.replaceChildren(...rows);
    }

    async function load(mode) {
      syncScope();
      if (!scope || blocked || busy || mode === 'older' && (!cursor || finished)) return;
      const generation = epoch, requestScope = { ...scope };
      const before = mode === 'older' ? cursor : '';
      busy = true;
      message = '正在加载单聊历史…';
      render();
      try {
        const url = '/api/v1/direct/' + requestScope.peerID + '/messages?limit=20' +
          (before ? '&before_message_id=' + before : '');
        const response = await fetch(url, { headers: { Authorization: 'Bearer ' + requestScope.token } });
        if (!current(generation)) return;
        if (response && (response.status === 401 || response.status === 403)) { deny(); return; }
        if (!response || !response.ok || response.status !== 200) throw new Error();
        const body = await response.json();
        if (!current(generation)) return;
        const page = validHistory(body, requestScope);
        if (!page) throw new Error();
        showHistory(page.messages, mode);
        pendingIDs = page.received;
        count = null;
        if (mode !== 'latest' || cursor === '') {
          cursor = page.next;
          finished = cursor === '0';
        }
        message = page.messages.length ? '已加载本页；只可显式确认本页收到的消息。'
          : '本页没有消息；可刷新最新消息或重新遍历。';
      } catch (_) {
        if (!current(generation)) return;
        message = '历史读取失败，翻页位置和本次确认目标已保留，可手动重试。';
      } finally {
        if (current(generation)) { busy = false; render(); }
      }
    }

    function validUnread(body, requestScope, ids) {
      const data = body && body.data;
      if (!body || body.code !== 0 || !data || data.peer_id !== requestScope.peerID || !validCount(data.unread_count)) return null;
      if (ids) {
        if (!Array.isArray(data.message_ids) || data.message_ids.length !== ids.length ||
            !data.message_ids.every(validID) || new Set(data.message_ids).size !== ids.length) return null;
        const echoed = sortIDs(data.message_ids);
        if (!echoed.every((id, index) => id === ids[index])) return null;
      } else if (Object.prototype.hasOwnProperty.call(data, 'message_ids')) return null;
      return data.unread_count;
    }

    async function unread(mark) {
      syncScope();
      if (!scope || blocked || busy || mark && pendingIDs.length === 0) return;
      const generation = epoch, requestScope = { ...scope }, ids = mark ? [...pendingIDs] : null;
      busy = true;
      count = null;
      message = mark ? '正在确认本页收到的消息…' : '正在刷新单聊未读数…';
      render();
      try {
        const headers = { Authorization: 'Bearer ' + requestScope.token };
        const init = { method: mark ? 'POST' : 'GET', headers };
        if (mark) {
          headers['Content-Type'] = 'application/json';
          init.body = JSON.stringify({ message_ids: ids });
        }
        const response = await fetch('/api/v1/direct/' + requestScope.peerID + (mark ? '/read' : '/unread'), init);
        if (!current(generation)) return;
        if (response && (response.status === 401 || response.status === 403)) { deny(); return; }
        if (!response || !response.ok || response.status !== 200) throw new Error();
        const body = await response.json();
        if (!current(generation)) return;
        const nextCount = validUnread(body, requestScope, ids);
        if (nextCount === null) throw new Error();
        count = nextCount;
        if (mark) pendingIDs = [];
        message = mark ? '本页收到的消息已确认；其他消息仍可能未读。'
          : '未读数已刷新；本页收到的消息仍需本人显式确认。';
      } catch (_) {
        if (!current(generation)) return;
        count = null;
        message = mark ? '确认结果不确定，请先刷新未读数，或用本页相同 ID 重试。'
          : '未读数读取失败，请手动重试。';
      } finally {
        if (current(generation)) { busy = false; render(); }
      }
    }

    latestButton.onclick = () => load('latest');
    restartButton.onclick = () => load('restart');
    olderButton.onclick = () => load('older');
    refreshButton.onclick = () => unread(false);
    readButton.onclick = () => unread(true);
    for (const id of ['token', 'toUserId', 'chatType']) {
      field(id).addEventListener('input', invalidate);
      field(id).addEventListener('change', invalidate);
    }
    invalidate();
    return Object.freeze({ invalidate, loadLatest: () => load('latest'), loadOlder: () => load('older'), restart: () => load('restart'), refresh: () => unread(false), mark: () => unread(true) });
  }

  globalThis.createDirectUnreadPage = createDirectUnreadPage;
  if (typeof document !== 'undefined') {
    globalThis.directUnreadPage = createDirectUnreadPage({ document, fetch: (...args) => globalThis.fetch(...args) });
  }
})();
