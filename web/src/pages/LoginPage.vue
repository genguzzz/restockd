<script setup lang="ts">
// 登录页：单管理员密码
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NCard, NForm, NFormItem, NInput, NText } from 'naive-ui'
import { authApi } from '@/api'
import { useAsync } from '@/composables/useAsync'

const password = ref('')
const router = useRouter()
const route = useRoute()
const { loading, run: login } = useAsync(() => authApi.login(password.value))

const submit = async () => {
  if (!password.value || !(await login())) return
  const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/'
  await router.replace(redirect)
}
</script>

<template>
  <div class="login">
    <n-card class="card" :bordered="false">
      <div class="brand"><span class="logo">R</span> restockd</div>
      <n-text depth="3">VPS 补货监控与自动锁单</n-text>
      <n-form style="margin-top: 20px" @submit.prevent="submit">
        <n-form-item label="管理员密码" :show-feedback="false">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="RESTOCKD_PASSWORD" autofocus @keyup.enter="submit" />
        </n-form-item>
        <n-button type="primary" block :loading="loading" :disabled="!password" style="margin-top: 18px" @click="submit">登录</n-button>
      </n-form>
    </n-card>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  box-sizing: border-box;
}
.card {
  width: min(380px, 100%);
  box-shadow: 0 10px 40px rgba(0, 0, 0, 0.08);
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 22px;
  font-weight: 700;
  margin-bottom: 4px;
}
.logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: #18a058;
  color: #fff;
  font-size: 17px;
}
</style>
