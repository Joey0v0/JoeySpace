<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorText, verifySession } from '../api/client.ts'
const router = useRouter()
const username = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')
async function submit() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.login(username.value.trim(), password.value)
    await verifySession()
    await router.replace('/messages')
  } catch (cause) { error.value = errorText(cause) }
  finally { password.value = ''; busy.value = false }
}
</script>
<template>
  <main class="task-preview">
    <form class="task-preview-card" @submit.prevent="submit">
      <p class="eyebrow">JOEYSPACE</p><h1>登录协作空间</h1>
      <p>使用已有账号登录，查看本人团队与会话。</p>
      <label>用户名<input v-model="username" name="username" autocomplete="username" required :disabled="busy" /></label>
      <label>密码<input v-model="password" name="password" type="password" autocomplete="current-password" required :disabled="busy" /></label>
      <p v-if="error" role="alert">{{ error }}</p>
      <button class="primary-link" type="submit" :disabled="busy">{{ busy ? '正在登录…' : '登录' }}</button>
    </form>
  </main>
</template>
<style scoped>
label { display:grid; gap:8px; margin:18px 0; font-size:14px }
input { padding:12px; border:1px solid #c8d3e3; border-radius:8px; width:100% }
button { border:0 } button:disabled { opacity:.6 }
[role=alert] { color:#a03030 }
</style>
