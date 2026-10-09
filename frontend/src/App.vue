<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { clearSession } from './auth/session.ts'
const route = useRoute()
const login = computed(() => route.path === '/login')
</script>
<template>
  <div class="app-shell">
    <nav v-if="!login" class="module-nav" aria-label="主导航">
      <RouterLink class="brand" to="/messages" aria-label="JoeySpace，返回会话总览">J<span>.</span></RouterLink>
      <div class="module-links">
        <RouterLink class="module-link" to="/messages" active-class="is-active"><span class="module-icon" aria-hidden="true">◌</span><span>消息</span></RouterLink>
        <RouterLink class="module-link" to="/tasks" active-class="is-active"><span class="module-icon" aria-hidden="true">✓</span><span>我的任务</span></RouterLink>
      </div>
      <button class="module-footer" type="button" @click="clearSession">退出登录</button>
    </nav>
    <RouterView />
  </div>
</template>
