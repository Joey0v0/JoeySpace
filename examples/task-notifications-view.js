(() => {
  'use strict';
  const field = id => document.getElementById(id);
  const readScope = () => {
    const token = field('token').value, teamID = field('teamId').value;
    if (typeof token !== 'string' || !token.trim() || typeof teamID !== 'string' ||
        !/^[1-9]\d{0,18}$/.test(teamID) || BigInt(teamID) > 9223372036854775807n) return null;
    return { token, teamID };
  };
  const timeFormat = new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai',
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
  const statusNames = ['待办', '进行中', '完成'];
  let page;
  page = new globalThis.TaskNotificationsController({ fetch: (...args) => fetch(...args), getScope: readScope,
    onChange: () => { if (page) render(); } });
  globalThis.taskNotificationsPage = page;

  function textNode(tag, text, parent) {
    const node = document.createElement(tag);
    node.textContent = text;
    if (parent) parent.appendChild(node);
    return node;
  }

  function render() {
    const state = page.state;
    field('btnRefreshTaskNotifications').disabled = state.loading || !readScope();
    field('btnMoreTaskNotifications').disabled = state.loading || !readScope() || !state.loaded || state.cursor === '0';
    let message;
    if (state.loading) message = '正在读取通知…';
    else if (state.error) message = state.error;
    else if (!readScope()) message = '请输入 Token 和有效的 Team ID，再刷新通知。';
    else if (!state.loaded) message = '尚未读取通知，请点击刷新通知。';
    else if (state.items.length === 0) message = '当前没有任务状态通知。';
    else if (state.cursor === '0') message = '已显示 ' + state.items.length + ' 条通知，没有更早通知。';
    else message = '已显示 ' + state.items.length + ' 条通知，可加载更早通知。';
    field('taskNotificationsStatus').textContent = message;
    const cards = state.items.map(item => {
      const card = document.createElement('article');
      card.className = 'task-card';
      textNode('strong', '任务 #' + item.task_id, card);
      textNode('p', '操作者 #' + item.actor_id + '：' + statusNames[item.from_status] + ' → ' + statusNames[item.to_status], card);
      textNode('p', '通知时间（Asia/Shanghai）：' + timeFormat.format(new Date(item.created_at_unix_ms)), card);
      return card;
    });
    field('taskNotificationsList').replaceChildren(...cards);
  }

  field('btnRefreshTaskNotifications').onclick = () => {
    if (!field('btnRefreshTaskNotifications').disabled) return page.refresh();
  };
  field('btnMoreTaskNotifications').onclick = () => {
    if (!field('btnMoreTaskNotifications').disabled) return page.loadMore();
  };
  for (const id of ['token', 'teamId']) field(id).addEventListener('input', () => { page.syncScope(); render(); });
  render();
})();
