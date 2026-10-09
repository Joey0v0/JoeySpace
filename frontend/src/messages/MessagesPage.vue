<script setup lang="ts">
import { computed, onUnmounted, reactive, watch } from 'vue'
import { useRoute } from 'vue-router'
import ConversationList from './ConversationList.vue'
import ConversationView from './ConversationView.vue'
import UnreadOverview from './UnreadOverview.vue'
import { createDirectory, initialDirectoryState } from './directory.ts'
import { api, currentProfile } from '../api/client.ts'
import { session } from '../auth/session.ts'
const route = useRoute()
const state = reactive(initialDirectoryState())
const directory = createDirectory(api.request, session, state)
const key = computed(() => {
  if (route.name === 'direct') return 'direct:' + String(route.params.peerId)
  if (route.name === 'group') return 'group:' + String(route.params.teamId) + ':' + String(route.params.groupId)
  return undefined
})
watch(key, value => { void directory.select(value) }, { immediate: true })
void directory.loadTeams()
void directory.loadDirects()
onUnmounted(directory.dispose)
</script>
<template>
  <main class="messages-layout">
    <ConversationList :state="state" :active-key="key" @teams="directory.loadTeams" @groups="directory.loadGroups" @directs="directory.loadDirects" />
    <div class="messages-workspace">
      <div class="sample-ribbon" role="note">{{ currentProfile?.nickname || currentProfile?.username }} · 真实会话导航</div>
      <UnreadOverview v-if="!key" />
      <section v-else-if="state.detailLoading" class="unavailable-state" role="status"><h1>正在复核会话…</h1><p>按当前登录身份检查访问权限。</p></section>
      <ConversationView v-else-if="state.current" :conversation="state.current" :joining="state.joining" :error="state.detailError" @join="directory.joinCurrent" />
      <section v-else class="unavailable-state">
        <div class="empty-symbol" aria-hidden="true">?</div><h1>会话暂时无法打开</h1>
        <p role="alert">{{ state.detailError || '请从左侧选择会话' }}</p>
        <button class="primary-link" type="button" @click="directory.select(key)">重新检查</button>
        <RouterLink class="primary-link" to="/messages">返回会话总览</RouterLink>
      </section>
    </div>
  </main>
</template>
