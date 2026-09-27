<script setup lang="ts">
import { ref } from 'vue'
import { operatorFetchJson, OperatorFetchError } from '../composables/useOperatorApi'

definePageMeta({ layout: false })

const { t, locale, setLocale } = useI18n()
const email = ref('')
const password = ref('')
const pending = ref(false)
const error = ref('')

async function signIn() {
  if (pending.value) return
  error.value = ''
  pending.value = true
  try {
    await operatorFetchJson('/api/auth/user-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email.value.trim(), password: password.value }),
    }, 'login')
    password.value = ''
    await navigateTo('/')
  } catch (cause) {
    password.value = ''
    error.value = cause instanceof OperatorFetchError && (cause.status === 401 || cause.status === 403)
      ? t('login.invalid')
      : t('login.unavailable')
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-panel" aria-labelledby="login-title">
      <div class="login-header">
        <div class="login-brand"><span class="login-glyph" aria-hidden="true">e</span> engram</div>
        <select :value="locale" :aria-label="t('login.language')" @change="setLocale(($event.target as HTMLSelectElement).value as 'ru' | 'en' | 'zh')">
          <option value="ru">Русский</option>
          <option value="en">English</option>
          <option value="zh">中文</option>
        </select>
      </div>
      <h1 id="login-title">{{ t('login.title') }}</h1>
      <p class="login-description">{{ t('login.description') }}</p>
      <form @submit.prevent="signIn">
        <label for="login-email">{{ t('login.email') }}</label>
        <input id="login-email" v-model="email" type="email" name="email" autocomplete="username" required autofocus />
        <label for="login-password">{{ t('login.password') }}</label>
        <input id="login-password" v-model="password" type="password" name="password" autocomplete="current-password" required />
        <p v-if="error" class="login-error" role="alert">{{ error }}</p>
        <button type="submit" :disabled="pending">{{ pending ? t('login.submitting') : t('login.submit') }}</button>
      </form>
    </section>
  </main>
</template>

<style scoped>
.login-page { min-height: 100%; overflow-y: auto; display: grid; place-items: center; padding: var(--space-6); background: var(--bg); }
.login-panel { width: min(100%, 420px); padding: var(--space-8); border: 1px solid var(--border); border-radius: var(--r-lg); background: var(--surface); }
.login-header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
.login-brand { display: flex; align-items: center; gap: var(--space-2); font-size: var(--text-lg); font-weight: 700; }
.login-glyph { display: grid; place-items: center; width: 30px; height: 30px; border-radius: var(--r-sm); background: var(--accent); color: var(--accent-on); }
.login-header select { max-width: 120px; padding: var(--space-2); border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--surface); color: var(--fg); }
h1 { margin: var(--space-8) 0 var(--space-2); font-size: var(--text-2xl); letter-spacing: var(--tracking-display); }
.login-description { margin: 0 0 var(--space-6); color: var(--fg-2); }
form { display: grid; gap: var(--space-2); }
label { font-size: var(--text-sm); font-weight: 600; }
input { width: 100%; min-height: 44px; margin-bottom: var(--space-3); padding: var(--space-3); border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--surface-warm); color: var(--fg); }
button { min-height: 44px; border: 0; border-radius: var(--r-sm); background: var(--accent); color: var(--accent-on); font-weight: 700; cursor: pointer; }
button:disabled { opacity: .65; cursor: wait; }
.login-error { margin: 0 0 var(--space-2); color: var(--danger); }
</style>
