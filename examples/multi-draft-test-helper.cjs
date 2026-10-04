const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function element(tagName = 'div') {
  return { tagName: tagName.toUpperCase(), value: '', textContent: '', disabled: false, dataset: {}, style: {}, children: [], attributes: {},
    appendChild(child) { this.children.push(child); return child; },
    replaceChildren(...children) { this.children = children; if (this.tagName === 'SELECT') this.value = children[0]?.value || ''; },
    setAttribute(name, value) { this.attributes[name] = String(value); },
    addEventListener(name, callback) { this['on' + name] = callback; } };
}

function page(fetch, scripts = ['multi-draft-core.js', 'multi-draft-actions.js']) {
  const html = fs.readFileSync(path.join(__dirname, 'chat.html'), 'utf8');
  const fields = {};
  for (const match of html.matchAll(/<(\w+)[^>]*\bid="([^"]+)"[^>]*>/g)) fields[match[2]] = element(match[1]);
  for (const id of ['multiDraftInstruction', 'multiDraftRequestKey', 'multiDraftRunID', 'multiDraftReference', 'multiDraftMessage',
    'multiDraftSummary', 'multiDraftItems', 'btnMultiPrepare', 'btnMultiLoad', 'btnMultiNewKey', 'btnMultiMembers', 'btnMultiMoreMembers']) {
    fields[id] ||= element(id.startsWith('btn') ? 'button' : 'div');
  }
  Object.assign(fields.token, { value: 'test-token' });
  Object.assign(fields.teamId, { value: '200' });
  Object.assign(fields.toUserId, { value: '300' });
  Object.assign(fields.chatType, { value: '2' });
  let random = 0;
  const context = { fetch, location: { protocol: 'http:', hostname: 'gateway.test' },
    crypto: { getRandomValues(bytes) { bytes.fill(++random); return bytes; } },
    document: { getElementById: id => fields[id], createElement: tag => element(tag) },
    encodeURIComponent, console };
  vm.createContext(context);
  vm.runInContext(html.match(/<script>([\s\S]*?)<\/script>/)[1], context);
  for (const script of scripts) vm.runInContext(fs.readFileSync(path.join(__dirname, script), 'utf8'), context, { filename: script });
  function controller(options = {}) {
    return new context.MultiDraftController({ fetch, getScope: () => context.taskDraftScope(),
      isScopeCurrent: scope => context.sameTaskDraftScope(scope), onChange: () => {}, now: () => 1791000000123,
      randomKey: () => 'collection-' + (++random), ...options });
  }
  return { context, fields, controller };
}

function draft(index = 0, overrides = {}) {
  return { item_index: index, status: 'waiting_confirmation', task_id: '0', reply_status: 'not_started', reply_msg_id: '',
    draft: { title: 'Task ' + index, description: '', revision: '1', assignee_id: '0', assignee_name: '', assignee_resolution: 'none',
      due_at_unix_ms: 0, source_message_id: '0', deadline: { text: '', source: 'none', source_message_id: '0', reference_unix_ms: 0,
        timezone: 'Asia/Shanghai', resolution: 'none', reason: '', parsed_unix_ms: 0, instruction_reference_unix_ms: 1791000000123 } },
    ...overrides };
}
function collection(items = [draft(0), draft(1)], overrides = {}) {
  return { run_id: '9007199254741011', team_id: '200', group_id: '300', item_count: items.length, items, ...overrides };
}
function itemData(item, overrides = {}) {
  return { run_id: '9007199254741011', team_id: '200', group_id: '300', item_count: 2, item, ...overrides };
}
function reply(data, status = 200) { return { ok: status >= 200 && status < 300, status, json: async () => ({ code: status === 200 ? 0 : 1000, msg: 'test result', data }) }; }
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function plain(value) { return JSON.parse(JSON.stringify(value)); }
module.exports = { page, draft, collection, itemData, reply, deferred, plain, element };
