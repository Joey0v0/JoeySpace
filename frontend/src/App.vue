<script setup lang="ts">
import { computed, onUnmounted, shallowRef } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { clearSession } from './auth/session.ts'
import { verification } from './auth/verification.ts'
import { taskSignal } from './realtime/taskSignal.ts'
const route = useRoute()
const login = computed(() => route.path === '/login')
const status = shallowRef({ ...verification.state })
const unsubscribe = verification.subscribe(() => { status.value = { ...verification.state } })
const taskPending = shallowRef(taskSignal.pending())
const unsubscribeSignal = taskSignal.subscribe(() => { taskPending.value = taskSignal.pending() })
onUnmounted(() => { unsubscribe(); unsubscribeSignal() })
const reviewing = computed(() => !login.value && !!status.value.error)
</script>
<template>
  <div class="app-shell">
    <nav v-if="!login && !reviewing" class="module-nav" aria-label="主导航">
      <RouterLink class="brand" to="/messages" aria-label="JoeySpace，返回会话总览">J<span>.</span></RouterLink>
      <div class="module-links">
        <RouterLink class="module-link" to="/messages" active-class="is-active"><span class="module-icon" aria-hidden="true">◌</span><span>消息</span></RouterLink>
        <RouterLink class="module-link task-nav-link" to="/tasks" active-class="is-active"><span class="module-icon" aria-hidden="true">✓</span><span>我的任务</span><span v-if="taskPending" class="task-nav-signal"><span class="sr-only">有新的任务通知</span></span></RouterLink>
      </div>
      <button class="module-footer" type="button" @click="clearSession">退出登录</button>
    </nav>
    <main v-if="reviewing" class="task-preview">
      <section class="task-preview-card">
        <p class="eyebrow">JOEYSPACE</p><h1>{{ status.busy ? '正在复核登录身份…' : '暂时无法复核登录身份' }}</h1>
        <p v-if="status.error" role="alert">{{ status.error }}</p>
        <p>当前会话地址已保留。身份复核成功后继续打开该页面。</p>
        <button class="primary-link" type="button" :disabled="status.busy" @click="verification.retry">{{ status.busy ? '正在检查…' : '重试身份复核' }}</button>
        <button type="button" :disabled="status.busy" @click="clearSession">退出登录</button>
      </section>
    </main>
    <RouterView v-else />
  </div>
</template>
