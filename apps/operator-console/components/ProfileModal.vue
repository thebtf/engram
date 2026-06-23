<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import type { ShellInfo } from '../composables/useOperatorShell'

const props = defineProps<{
  open: boolean
  info: ShellInfo
  authPostureLabel: string
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const { t } = useI18n()
const previousBodyOverflow = ref<string | null>(null)

function close() {
  emit('update:open', false)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') close()
}

watch(() => props.open, (isOpen) => {
  if (!import.meta.client) return
  if (isOpen) {
    if (previousBodyOverflow.value === null) {
      previousBodyOverflow.value = document.body.style.overflow
    }
    document.body.style.overflow = 'hidden'
    window.addEventListener('keydown', onKeydown)
  } else {
    if (previousBodyOverflow.value !== null) {
      document.body.style.overflow = previousBodyOverflow.value
      previousBodyOverflow.value = null
    }
    window.removeEventListener('keydown', onKeydown)
  }
}, { immediate: true })

onBeforeUnmount(() => {
  if (!import.meta.client) return
  window.removeEventListener('keydown', onKeydown)
  if (previousBodyOverflow.value !== null) {
    document.body.style.overflow = previousBodyOverflow.value
    previousBodyOverflow.value = null
  }
})
</script>

<template>
  <div v-if="open" class="modal-host" @click.self="close">
    <section class="modal profile-modal" role="dialog" aria-modal="true" :aria-label="t('profileModal.aria')">
      <div class="profile-shell">
        <div class="profile-hero">
          <span class="iav">{{ info.identityInitials }}</span>
          <div class="profile-id">
            <h2>{{ info.identityName }}</h2>
            <p>{{ t('profileModal.via', { provider: info.identityProvider }) }} · {{ authPostureLabel }}</p>
          </div>
          <button class="tbtn close" type="button" :aria-label="t('profileModal.close')" @click="close">×</button>
        </div>

        <div class="profile-body">
          <section class="profile-section">
            <div class="profile-section-title">
              <span>{{ t('profileModal.profileTitle') }}</span>
              <HonestyBadge cls="mustbuild" evidence="PATCH /api/profile" />
            </div>
            <div class="profile-row">
              <div>
                <b>{{ t('profileModal.displayName') }}</b>
                <p>{{ t('profileModal.displayNameDesc') }}</p>
              </div>
              <span class="readonly">{{ info.identityName }}</span>
            </div>
            <div class="profile-row">
              <div>
                <b>{{ t('profileModal.role') }}</b>
                <p>{{ t('profileModal.roleDesc') }}</p>
              </div>
              <span class="readonly">{{ info.role }}</span>
            </div>
            <div class="mustbuild-note">
              <b>{{ t('profileModal.profileMustBuildTitle') }}</b>
              <span>{{ t('profileModal.profileMustBuildBody') }}</span>
            </div>
          </section>

          <section id="pfSessions" class="profile-section">
            <div class="profile-section-title">
              <span>{{ t('profileModal.sessionsTitle') }}</span>
              <span class="sess-count">{{ t('profileModal.sessionsCount', 1) }}</span>
            </div>
            <div class="psession">
              <span class="ps-ic">◐</span>
              <span class="ps-meta">
                <b>{{ t('profileModal.currentSession') }}</b>
                <span>{{ info.host }} · {{ info.identityProvider }}</span>
              </span>
              <span class="ps-cur">{{ t('profileModal.current') }}</span>
            </div>
            <div class="mustbuild-note">
              <HonestyBadge cls="mustbuild" evidence="GET /api/auth/sessions" />
              <span>{{ t('profileModal.sessionsMustBuildBody') }}</span>
            </div>
          </section>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.modal-host {
  position:fixed;
  inset:0;
  z-index:80;
  display:grid;
  place-items:center;
  padding:24px;
  background:color-mix(in oklab, #000, transparent 34%);
}
.modal.profile-modal {
  width:min(640px, calc(100vw - 48px));
  max-height:min(760px, 88vh);
  overflow:hidden;
  border:1px solid var(--border);
  border-radius:var(--r-lg);
  background:var(--surface);
  box-shadow:var(--elev-raised);
  color:var(--fg);
}
.profile-shell { width:100%; }
.profile-hero { display:flex; gap:14px; align-items:center; padding:var(--space-5); border-bottom:1px solid var(--border); }
.profile-hero .iav { width:54px; height:54px; border-radius:50%; display:grid; place-items:center; color:#fff; font-weight:800; font-size:20px; background:var(--accent); flex:none; overflow:hidden; }
.profile-id { flex:1; min-width:0; }
.profile-id h2 { margin:0; font-size:var(--text-xl); font-weight:800; }
.profile-id p { margin:3px 0 0; color:var(--muted); font-size:var(--text-sm); font-family:var(--font-mono); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.tbtn.close { width:34px; height:34px; justify-content:center; padding:0; font-size:18px; }
.profile-body { max-height:min(560px,70vh); overflow:auto; padding:var(--space-2) var(--space-5) var(--space-5); }
.profile-section { padding:var(--space-4) 0; border-top:1px solid var(--border-soft); }
.profile-section:first-child { border-top:0; }
.profile-section-title { display:flex; align-items:center; justify-content:space-between; gap:8px; margin-bottom:var(--space-3); color:var(--muted); font-size:var(--text-xs); text-transform:uppercase; letter-spacing:.08em; font-weight:800; }
.profile-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:14px; align-items:center; padding:11px 0; border-top:1px solid var(--border-soft); }
.profile-row:first-of-type { border-top:0; }
.profile-row b { display:block; color:var(--fg); font-size:var(--text-sm); }
.profile-row p { margin:3px 0 0; color:var(--muted); font-size:var(--text-xs); line-height:1.45; }
.readonly { font-family:var(--font-mono); color:var(--fg-2); font-size:var(--text-xs); border:1px solid var(--border); background:var(--surface-warm); border-radius:var(--radius-pill); padding:3px 8px; white-space:nowrap; }
.mustbuild-note { display:flex; align-items:flex-start; gap:10px; margin-top:var(--space-3); padding:11px 12px; border:1px dashed color-mix(in oklab, var(--class-mustbuild), transparent 45%); border-radius:var(--r-md); background:color-mix(in oklab, var(--class-mustbuild), transparent 91%); color:var(--fg-2); font-size:var(--text-xs); line-height:1.45; }
.mustbuild-note b { color:var(--fg); }
.mustbuild-note span { min-width:0; }
.psession { display:flex; align-items:center; gap:11px; padding:11px 0; border-top:1px solid var(--border-soft); }
.psession:first-child { border-top:0; }
.ps-ic { width:30px; height:30px; border-radius:8px; display:grid; place-items:center; background:var(--surface-warm); border:1px solid var(--border); color:var(--fg-2); flex:none; }
.ps-meta { flex:1; min-width:0; }
.ps-meta b { display:block; font-size:var(--text-sm); }
.ps-meta span { display:block; font-size:var(--text-xs); color:var(--muted); font-family:var(--font-mono); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.ps-cur { font-size:10px; font-weight:800; color:var(--success); text-transform:uppercase; letter-spacing:.05em; }
.sess-count { font-family:var(--font-mono); font-size:10px; font-weight:700; line-height:1; padding:2px 7px; border-radius:var(--radius-pill); background:var(--surface-warm); border:1px solid var(--border); color:var(--fg-2); text-transform:none; letter-spacing:0; }
.profile-body::-webkit-scrollbar { width:8px; height:8px; }
.profile-body::-webkit-scrollbar-track { background:transparent; }
.profile-body::-webkit-scrollbar-thumb { background:color-mix(in oklab,var(--border),transparent 40%); border-radius:var(--radius-pill); }
.profile-body::-webkit-scrollbar-thumb:hover { background:color-mix(in oklab,var(--border),transparent 10%); }
@media (max-width:700px) {
  .modal-host { padding:12px; align-items:start; }
  .modal.profile-modal { width:100%; }
  .profile-row { grid-template-columns:1fr; }
}
</style>
