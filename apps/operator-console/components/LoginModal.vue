<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useOperatorShellStatus } from '../composables/useOperatorShell'
import { toOperatorSourceError, type OperatorSourceError } from '../composables/useOperatorApi'

const emit = defineEmits<{ close: []; authenticated: [] }>()
const { t } = useI18n()
const shell = useOperatorShellStatus()
const dialog = ref<HTMLDialogElement | null>(null)
const emailInput = ref<HTMLInputElement | null>(null)
const email = ref('')
const password = ref('')
const pending = ref(false)
const error = ref<OperatorSourceError | null>(null)
const unverified = ref(false)
let closed = false
let trigger: HTMLElement | null = null

const errorKey = computed(() => {
  if (unverified.value) return 'auth.unverified'
  switch (error.value?.status) {
    case 400: return 'auth.invalidInput'
    case 401: return 'auth.invalidCredentials'
    case 403: return 'auth.disabledAccount'
    case 429: return 'auth.rateLimited'
    case 404: case 405: return 'auth.loginUnavailable'
    default: return error.value?.status && error.value.status >= 500 ? 'auth.serverUnavailable' : 'auth.unreachable'
  }
})

function close() {
  closed = true
  password.value = ''
  email.value = ''
  emit('close')
}

async function submit() {
  if (pending.value) return
  pending.value = true
  error.value = null
  unverified.value = false
  const attempt = shell.login(email.value.trim(), password.value)
  password.value = ''
  email.value = ''
  try {
    const confirmed = await attempt
    if (closed) return
    if (confirmed) emit('authenticated')
    else unverified.value = true
  } catch (cause) {
    if (!closed) error.value = toOperatorSourceError(cause, { source: 'shell-user-login', path: '/api/auth/user-login', method: 'POST' })
  } finally {
    pending.value = false
    password.value = ''
  }
}

onMounted(() => {
  trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  dialog.value?.showModal()
  emailInput.value?.focus()
})
onBeforeUnmount(() => {
  closed = true
  password.value = ''
  email.value = ''
  dialog.value?.close()
  if (trigger?.isConnected) trigger.focus()
})
</script>

<template>
  <dialog ref="dialog" class="login-dialog" aria-labelledby="login-title" aria-describedby="login-description" @cancel.prevent="close" @click="($event.target === dialog) && close()">
    <header><h2 id="login-title">{{ t('auth.signIn') }}</h2><button type="button" :aria-label="t('auth.close')" @click="close">×</button></header>
    <p id="login-description">{{ t('auth.description') }}</p>
    <form :aria-busy="pending" @submit.prevent="submit">
      <label for="login-email">{{ t('auth.email') }}</label>
      <input id="login-email" ref="emailInput" v-model="email" name="email" type="email" autocomplete="username" inputmode="email" required :disabled="pending" />
      <label for="login-password">{{ t('auth.password') }}</label>
      <input id="login-password" v-model="password" name="password" type="password" autocomplete="current-password" required :disabled="pending" />
      <p v-if="error || unverified" class="login-error" role="alert">{{ t(errorKey) }}</p>
      <p v-if="pending" role="status">{{ t('auth.pending') }}</p>
      <div class="login-actions"><button type="button" @click="close">{{ t('auth.cancel') }}</button><button class="primary" type="submit" :disabled="pending">{{ t('auth.signIn') }}</button></div>
    </form>
  </dialog>
</template>

<style scoped>
.login-dialog { width:min(440px,calc(100vw - 32px)); max-height:calc(100dvh - 32px); margin:auto; padding:var(--space-5); overflow:auto; color:var(--fg); background:var(--surface); border:1px solid var(--border); border-radius:var(--r-lg); box-shadow:var(--elev-raised); }
.login-dialog::backdrop { background:color-mix(in oklab,#000,transparent 34%); }
header { display:flex; align-items:center; justify-content:space-between; gap:12px; }
h2 { margin:0; font-size:var(--text-xl); }
p { line-height:1.5; }
form { display:grid; gap:12px; }
label { font-weight:600; }
input { width:100%; min-width:0; min-height:44px; padding:10px; font-size:16px; color:var(--fg); background:var(--surface-warm); border:1px solid var(--border); border-radius:var(--r-sm); }
button { min-height:44px; padding:10px 14px; border:1px solid var(--border); border-radius:var(--r-sm); color:var(--fg); background:var(--surface-warm); cursor:pointer; }
button:disabled { opacity:.6; cursor:wait; }
button.primary { background:var(--accent); color:var(--accent-on); }
.login-actions { display:flex; justify-content:flex-end; gap:12px; }
.login-error { color:var(--state-warn); }
button:focus-visible, input:focus-visible { outline:2px solid var(--accent); outline-offset:3px; }
</style>
