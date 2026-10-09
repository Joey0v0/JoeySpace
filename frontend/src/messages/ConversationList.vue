<script setup lang="ts">
import { computed, ref } from 'vue'
import type { initialDirectoryState } from './directory.ts'
const props = defineProps<{ state: ReturnType<typeof initialDirectoryState>; activeKey?: string }>()
defineEmits<{ teams: []; groups: [teamId: string]; directs: [] }>()
const search = ref('')
const expanded = ref<Record<string, boolean>>({})
const needle = computed(() => search.value.trim().toLocaleLowerCase())
const matches = (name: string) => name.toLocaleLowerCase().includes(needle.value)
const directs = computed(() => props.state.directs.items.filter(item => matches(item.display_name)))
function toggle(teamId: string) { expanded.value[teamId] = !expanded.value[teamId] }
</script>
<template>
  <aside class="conversation-panel" aria-label="会话列表">
    <div class="conversation-panel-header"><div class="section-overline">WORKSPACE</div><h1>消息<span class="heading-period">.</span></h1><p>团队讨论与直接消息</p></div>
    <div class="directory-controls">
      <RouterLink to="/messages" class="overview-entry" :class="{ 'is-active': !activeKey }" :aria-current="!activeKey ? 'page' : undefined">
        <span class="overview-entry-icon" aria-hidden="true">▦</span><span class="overview-entry-text"><strong>会话总览</strong><small>找到团队讨论与已有私聊</small></span>
      </RouterLink>
      <div class="search-wrap"><span aria-hidden="true">⌕</span><input v-model="search" type="search" aria-label="搜索已加载会话" placeholder="搜索已加载会话" autocomplete="off" /></div>
      <p class="directory-hint">仅搜索已加载项；展开团队加载群聊</p>
    </div>
    <div class="directory-scroll">
      <section class="directory-section" aria-label="本人团队">
        <h2>团队群聊</h2>
        <p v-if="state.teams.loading" class="directory-empty" role="status">正在加载团队…</p>
        <div v-if="state.teams.error" class="directory-error" role="alert">{{ state.teams.error }} <button type="button" :disabled="state.teams.loading" @click="$emit('teams')">重试</button></div>
        <p v-if="state.teams.loaded && !state.teams.items.length && !state.teams.error" class="directory-empty">尚未加入团队</p>
        <section v-for="team in state.teams.items" :key="team.team_id" class="team-directory">
          <button class="team-toggle" type="button" :aria-expanded="!!expanded[team.team_id]" @click="toggle(team.team_id); expanded[team.team_id] && $emit('groups', team.team_id)">{{ expanded[team.team_id] ? '▾' : '▸' }} {{ team.name }}</button>
          <template v-if="expanded[team.team_id]">
            <p v-if="state.groups[team.team_id]?.loading" class="directory-empty" role="status">正在加载群聊…</p>
            <div v-if="state.groups[team.team_id]?.error" class="directory-error" role="alert">{{ state.groups[team.team_id]?.error }} <button type="button" :disabled="state.groups[team.team_id]?.loading" @click="$emit('groups', team.team_id)">重试</button></div>
            <template v-for="group in state.groups[team.team_id]?.items ?? []" :key="group.group_id">
              <RouterLink v-if="matches(group.name) || matches(team.name)" :to="'/messages/teams/' + team.team_id + '/groups/' + group.group_id" class="conversation-row" :class="{ 'is-active': activeKey === 'group:' + team.team_id + ':' + group.group_id }" :aria-current="activeKey === 'group:' + team.team_id + ':' + group.group_id ? 'page' : undefined">
                <span class="avatar avatar-group" aria-hidden="true">#</span><span class="conversation-row-copy"><span class="row-title">{{ group.name }}</span><small>{{ group.joined ? '已加入' : '可发现 · 尚未加入' }}</small></span>
              </RouterLink>
            </template>
            <p v-if="state.groups[team.team_id]?.loaded && !state.groups[team.team_id]?.items.length && !state.groups[team.team_id]?.error" class="directory-empty">该团队暂无群聊</p>
            <p v-else-if="state.groups[team.team_id]?.loaded && state.groups[team.team_id]?.items.length && !(state.groups[team.team_id]?.items ?? []).some(group => matches(group.name) || matches(team.name))" class="directory-empty">已加载群聊中没有匹配项</p>
            <button v-if="state.groups[team.team_id]?.loaded && state.groups[team.team_id]?.cursor !== '0'" class="directory-more" type="button" :disabled="state.groups[team.team_id]?.loading" @click="$emit('groups', team.team_id)">更多群聊</button>
          </template>
        </section>
        <button v-if="state.teams.loaded && state.teams.cursor !== '0'" class="directory-more" type="button" :disabled="state.teams.loading" @click="$emit('teams')">更多团队</button>
      </section>
      <section class="directory-section" aria-label="本人私聊">
        <h2>私聊</h2>
        <p v-if="state.directs.loading" class="directory-empty" role="status">正在加载私聊…</p>
        <div v-if="state.directs.error" class="directory-error" role="alert">{{ state.directs.error }} <button type="button" :disabled="state.directs.loading" @click="$emit('directs')">重试</button></div>
        <RouterLink v-for="item in directs" :key="item.peer_id" :to="'/messages/direct/' + item.peer_id" class="conversation-row" :class="{ 'is-active': activeKey === 'direct:' + item.peer_id }" :aria-current="activeKey === 'direct:' + item.peer_id ? 'page' : undefined">
          <span class="avatar avatar-direct" aria-hidden="true">{{ item.display_name.slice(0, 1) }}</span><span class="conversation-row-copy"><span class="row-title">{{ item.display_name }}</span><small>持久私聊会话</small></span>
        </RouterLink>
        <p v-if="state.directs.loaded && !directs.length && !state.directs.error" class="directory-empty">{{ needle ? '已加载私聊中没有匹配项' : '尚无持久私聊记录' }}</p>
        <button v-if="state.directs.loaded && state.directs.cursor !== '0'" class="directory-more" type="button" :disabled="state.directs.loading" @click="$emit('directs')">更多私聊</button>
      </section>
    </div>
    <div class="directory-footer">未读信息将在聊天接线后显示</div>
  </aside>
</template>
