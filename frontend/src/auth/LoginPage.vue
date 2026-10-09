<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorText, verifySession } from '../api/client.ts'
const router = useRouter()
const username = ref('')
const password = ref('')
const nickname = ref('')
const registering = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
function changeMode() { registering.value = !registering.value; password.value = ''; error.value = ''; notice.value = '' }
async function submit() {
  if (busy.value) return
  busy.value = true
  error.value = ''; notice.value = ''
  try {
    if (registering.value) {
      await api.register(username.value.trim(), password.value, nickname.value.trim())
      registering.value = false
      notice.value = '注册成功，请使用新账号登录'
    } else {
      await api.login(username.value.trim(), password.value)
      await verifySession()
      await router.replace('/messages')
    }
  } catch (cause) { error.value = errorText(cause) }
  finally { password.value = ''; busy.value = false }
}
</script>
<template>
  <main class="task-preview">
    <form class="task-preview-card" @submit.prevent="submit">
      <p class="eyebrow">JOEYSPACE</p><h1>{{ registering ? '注册协作账号' : '登录协作空间' }}</h1>
      <p>{{ registering ? '用户名 3—32 字，密码 6—64 字且不超过 72 字节。' : '使用已有账号登录，查看本人团队与会话。' }}</p>
      <label>用户名<input v-model="username" name="username" autocomplete="username" required :disabled="busy" /></label>
      <label>密码<input v-model="password" name="password" type="password" :autocomplete="registering ? 'new-password' : 'current-password'" required :disabled="busy" /></label>
      <label v-if="registering">昵称（可选）<input v-model="nickname" name="nickname" autocomplete="nickname" :disabled="busy" /></label>
      <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
      <button class="primary-link" type="submit" :disabled="busy">{{ busy ? '正在提交…' : registering ? '注册账号' : '登录' }}</button>
      <button class="mode-switch" type="button" :disabled="busy" @click="changeMode">{{ registering ? '已有账号，返回登录' : '没有账号？注册' }}</button>
    </form>
  </main>
</template>
<style scoped>
label { display:grid; gap:8px; margin:18px 0; font-size:14px }
input { padding:12px; border:1px solid #c8d3e3; border-radius:8px; width:100% }
button { border:0 } button:disabled { opacity:.6 }
.mode-switch { margin-left:12px; padding:10px; background:transparent; color:#3859c7 }
[role=alert] { color:#a03030 }
</style>
