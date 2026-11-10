<script setup lang="ts">
/**
 * RevealSecret — inline one-shot reveal of a write-only secret.
 * Matches the live VaultView behaviour exactly: masked by default, revealed only on
 * explicit action, auto-re-masks after `seconds`, copy + manual hide. The revealed
 * value lives ONLY in this transient component state — never persist it, never lift it
 * into a store (Secrets-are-write-only invariant, .od/DEVELOPER-PLAYBOOK §0).
 *
 * The parent fetches the value on demand (e.g. useVault().revealCredential(name)),
 * passes it in, and drops it when @hide fires.
 *
 * <RevealSecret v-if="revealed" :value="revealed" :seconds="30" @hide="onHide" @copy="onCopy" />
 */
import { ref, onMounted, onBeforeUnmount } from 'vue'

const props = withDefaults(defineProps<{ value: string; seconds?: number }>(), { seconds: 30 })
const emit = defineEmits<{ hide: []; copy: [] }>()
const { t } = useI18n()

const left = ref(props.seconds)
const copied = ref(false)
const copyError = ref(false)
let active = true
let copyTimer: ReturnType<typeof setTimeout> | null = null
let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  timer = setInterval(() => { if (--left.value <= 0) hide() }, 1000)
})
onBeforeUnmount(() => { active = false; if (timer) clearInterval(timer); if (copyTimer) clearTimeout(copyTimer) })

function hide() { if (timer) { clearInterval(timer); timer = null } emit('hide') }
function fallbackCopy(value: string): boolean {
  const input = document.createElement('textarea')
  input.value = value
  input.setAttribute('readonly', '')
  input.style.position = 'fixed'
  input.style.left = '-9999px'
  document.body.appendChild(input)
  try {
    input.select()
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    input.remove()
  }
}

async function copy() {
  copied.value = false
  copyError.value = false
  if (copyTimer) clearTimeout(copyTimer)
  let success = false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(props.value)
      success = true
    }
  } catch { }
  if (!active) return
  if (!success) success = fallbackCopy(props.value)
  if (!success) { copyError.value = true; return }
  copied.value = true
  emit('copy')
  copyTimer = setTimeout(() => { copied.value = false; copyTimer = null }, 2000)
}
</script>

<template>
  <div class="reveal" @click.stop>
    <code class="rv-key" :title="t('secrets.revealTitle')">{{ value }}</code>
    <span class="rv-timer">{{ t('secrets.revealTimer', { seconds: left }) }}</span>
    <button class="rv-ic" :aria-label="t('secrets.copySecret')" @click="copy">
      <svg v-if="!copied" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <rect width="14" height="14" x="8" y="8" rx="2" />
        <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
      </svg>
      <svg v-else width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round" style="color:var(--reveal-key)">
        <path d="M20 6 9 17l-5-5" />
      </svg>
    </button>
    <button class="rv-ic" :aria-label="t('secrets.hideSecret')" @click="hide">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <path d="M10.7 5.1a10.7 10.7 0 0 1 11.2 6.6 1 1 0 0 1 0 .7 10.7 10.7 0 0 1-1.4 2.5" />
        <path d="M14 14.2a3 3 0 0 1-4.2-4.3" />
        <path d="M17.5 17.5A10.8 10.8 0 0 1 2 12.3a1 1 0 0 1 0-.7 10.8 10.8 0 0 1 4.4-5.1" />
        <path d="m2 2 20 20" />
      </svg>
    </button>
    <span v-if="copyError" class="rv-error" role="alert">{{ t('secrets.copyFailed') }}</span>
  </div>
</template>

<style scoped>
.reveal {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  animation: rvIn var(--motion-fast) var(--ease-standard);
}

.rv-key {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--reveal-key);
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--surface-warm);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-width: 20rem;
  user-select: all;
}

.rv-timer {
  font-size: 10px;
  color: var(--reveal-timer);
  white-space: nowrap;
}

.rv-ic {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  flex: none;
  border: 0;
  border-radius: var(--r-sm);
  background: transparent;
  color: var(--muted);
  cursor: pointer;
}

.rv-error {
  color: var(--state-warn);
  font-size: var(--text-xs);
}

.rv-ic:hover {
  color: var(--fg-2);
  background: var(--surface-warm);
}

@keyframes rvIn {
  from {
    opacity: 0
  }

  to {
    opacity: 1
  }
}
</style>
