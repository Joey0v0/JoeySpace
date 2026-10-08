<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'
</script>

<template>
  <div class="app-shell">
    <nav class="module-nav" aria-label="主导航">
      <RouterLink class="brand" to="/messages" aria-label="JoeySpace，返回消息总览">J<span>.</span></RouterLink>
      <div class="module-links">
        <RouterLink class="module-link" to="/messages" active-class="is-active">
          <span class="module-icon" aria-hidden="true">◌</span><span>消息</span>
        </RouterLink>
        <RouterLink class="module-link" to="/tasks" active-class="is-active">
          <span class="module-icon" aria-hidden="true">✓</span><span>我的任务</span>
        </RouterLink>
      </div>
      <div class="module-footer" title="界面样例">预览</div>
    </nav>
    <RouterView />
  </div>
</template>
