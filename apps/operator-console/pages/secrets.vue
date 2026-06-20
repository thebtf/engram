<script setup lang="ts">
/** Secrets — vault status (masked fingerprint) + credentials table with one-shot reveal.
 *  Seam: useCreds()/useVaultStatus() → GET /api/vault/*. Reveal value would come from
 *  POST-on-demand; here it is mocked. Never persist a revealed value. */
import { ref } from 'vue'
import { useCreds, useVaultStatus } from '../composables/useMockData'

const { t } = useI18n()   // worked example: every visible string in this page is keyed (secrets.*)
const creds = useCreds()
const vault = ref(useVaultStatus())
const fpShown = ref(false)
const revealed = ref<Record<string, string>>({})   // transient only — never lifted to a store
const rotateOpen = ref(false)
let fpTimer: ReturnType<typeof setTimeout> | null = null

function revealFp() {
  fpShown.value = !fpShown.value
  if (fpTimer) clearTimeout(fpTimer)
  if (fpShown.value) fpTimer = setTimeout(() => (fpShown.value = false), 30000)   // auto re-mask, like .od
}
function maskedFp() { return fpShown.value ? vault.value.fingerprint.replace(/(.{4})(?=.)/g, '$1 ').trim() : t('secrets.fingerprintMasked') }
function copyFp() { try { navigator.clipboard?.writeText(vault.value.fingerprint) } catch {} }
function reveal(id: string) {
  // DEVELOPER: await useVault().revealCredential(id) → real one-shot value
  revealed.value[id] = `sk-live-${id}••one-shot`
}
function hide(id: string) { delete revealed.value[id] }

function onRotate(_keys: { current: string; next: string }) {
  // DEVELOPER: POST /api/vault/rotate {current,next} → server re-encrypts all secrets and
  // returns the new fingerprint. Client never computes it and never persists either key.
  rotateOpen.value = false
  fpShown.value = false
  const hex = '0123456789abcdef'
  vault.value = { ...vault.value, fingerprint: Array.from({ length: 16 }, (_, i) => hex[(i * 7 + 3) % 16]).join('') }
}
</script>

<template>
  <div>
    <header class="head"><h1>{{ t('secrets.title') }}</h1><p>{{ t('secrets.subtitle') }}</p></header>

    <div class="vault-status">
      <p class="vs-title">{{ t('secrets.vaultState') }}</p>
      <div class="vs-grid">
        <div class="vs-field"><p class="vs-k">{{ t('secrets.encryption') }}</p><HonestyBadge cls="live" :label="t('common.enabled')" /></div>
        <div class="vs-field"><p class="vs-k">{{ t('secrets.fingerprint') }}</p>
          <div class="fp">
            <code>{{ maskedFp() }}</code>
            <button class="ic" :title="fpShown ? t('common.hide') : t('common.show')" @click="revealFp">{{ fpShown ? '🙈' : '👁' }}</button>
            <button v-if="fpShown" class="ic" :title="t('common.copy')" @click="copyFp">⧉</button>
            <button class="lnk" @click="rotateOpen = true">{{ t('common.change') }}</button>
          </div>
        </div>
        <div class="vs-field"><p class="vs-k">{{ t('secrets.count') }}</p><div class="vs-v">{{ creds.length }}</div></div>
      </div>
    </div>

    <KeyRotationModal v-if="rotateOpen" :secret-count="creds.length" :current-fp="vault.fingerprint"
      @rotate="onRotate" @close="rotateOpen = false" />

    <table class="vault-tbl">
      <thead><tr><th>{{ t('secrets.colKey') }}</th><th>{{ t('secrets.colProject') }}</th><th>{{ t('secrets.colScope') }}</th><th>{{ t('secrets.colCreated') }}</th><th class="r">{{ t('secrets.colActions') }}</th></tr></thead>
      <tbody>
        <template v-for="c in creds" :key="c.id">
          <tr>
            <td><div class="vn"><span class="vk">🔑</span>{{ c.id }}<RevealSecret v-if="revealed[c.id]" :value="revealed[c.id]" :seconds="30" @hide="hide(c.id)" /></div></td>
            <td><span class="vcol">{{ c.project }}</span></td>
            <td><span class="bdg">{{ c.scope }}</span></td>
            <td><span class="vcol">{{ c.created }}</span></td>
            <td class="r"><button v-if="!revealed[c.id]" class="act" @click="reveal(c.id)">{{ t('secrets.reveal') }}</button></td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.vault-status { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; margin-bottom:16px; }
.vs-title { margin:0 0 14px; font-size:var(--text-sm); color:var(--muted); }
.vs-grid { display:grid; grid-template-columns:repeat(3,1fr); gap:24px; }
.vs-k { margin:0 0 5px; font-size:var(--text-xs); color:var(--muted); }
.vs-v { font-size:var(--text-sm); font-family:var(--font-mono); }
.fp { display:flex; align-items:center; gap:6px; }
.fp code { font-family:var(--font-mono); font-size:var(--text-sm); letter-spacing:.04em; }
.fp .ic { border:0; background:transparent; cursor:pointer; }
.fp .lnk { margin-left:4px; border:0; background:transparent; color:var(--reveal-timer); cursor:pointer; font-size:var(--text-xs); font-weight:500; }
.fp .lnk:hover { text-decoration:underline; }
.vault-tbl { width:100%; border-collapse:collapse; border:1px solid var(--border); border-radius:var(--r-md); overflow:hidden; }
.vault-tbl th { text-align:left; font-size:var(--text-xs); font-weight:500; color:var(--muted); padding:10px 14px; border-bottom:1px solid var(--border); }
.vault-tbl th.r, .vault-tbl td.r { text-align:right; }
.vault-tbl td { padding:13px 14px; border-bottom:1px solid var(--border-soft); vertical-align:top; }
.vn { display:flex; flex-direction:column; font-size:var(--text-sm); font-weight:500; }
.vn .vk { margin-right:6px; }
.vcol { font-size:var(--text-xs); color:var(--muted); font-family:var(--font-mono); }
.bdg { font-size:10px; padding:2px 7px; border:1px solid var(--border); border-radius:5px; background:var(--surface-warm); color:var(--fg-2); }
.act { font-size:var(--text-sm); padding:5px 11px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg); cursor:pointer; }
.act:hover { background:var(--surface-warm); }
</style>
