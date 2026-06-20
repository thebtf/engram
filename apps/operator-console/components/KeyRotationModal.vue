<script setup lang="ts">
/**
 * KeyRotationModal — changing the vault fingerprint is NEVER a text edit; it is a key
 * rotation: decrypt every secret with the current key → re-encrypt under the new key →
 * derive the new fingerprint (not typed) → destroy the old key + audit. Mirrors the .od
 * openKeyRotationModal contract (DEVELOPER-PLAYBOOK §0).
 *
 * Requires current key (for decrypt) + new key ×2 (for encrypt) + explicit ack.
 * Seam: emit('rotate', {current,new}) → POST /api/vault/rotate. The server computes the
 * new fingerprint; the client never does and never persists either key.
 *
 * <KeyRotationModal v-if="open" :secret-count="n" :current-fp="fp" @rotate="..." @close="..." />
 */
import { ref, computed } from 'vue'

// i18n plural exemplar: t(key, n) selects the right Russian form via the Slavic pluralRule
// in i18n/i18n.config.ts. "5 секретов" not "5 секрет(ов)". (Other strings in this modal are
// still hardcoded — keying the whole component is tracked as the secrets-row i18n gap.)
const { t } = useI18n()
const props = defineProps<{ secretCount: number; currentFp: string }>()
const emit = defineEmits<{ rotate: [{ current: string; next: string }]; close: [] }>()

const cur = ref(''), next = ref(''), next2 = ref(''), ack = ref(false), showCur = ref(false)
const valid = computed(() => cur.value.trim() && next.value.trim() && next.value === next2.value && ack.value)

function rotate() {
  if (!valid.value) return
  emit('rotate', { current: cur.value, next: next.value })   // keys leave only here; never stored
}
</script>

<template>
  <div class="overlay" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true">
      <h2>Ротация ключа vault</h2>
      <div class="mb">
        <div class="warn">
          <span class="ic">⚠</span>
          <p><b>Опасное действие.</b> Отпечаток — производная от мастер-ключа, его нельзя «вписать». Смена перешифровывает <b>все {{ t('secrets.nSecrets', secretCount) }}</b>: каждый расшифровывается текущим ключом и заново шифруется новым. Неверный текущий ключ → расшифровка невозможна, секреты потеряны.</p>
        </div>
        <ol class="steps">
          <li>Текущий ключ расшифровывает каждый секрет.</li>
          <li>Каждый секрет заново шифруется новым ключом.</li>
          <li>Новый отпечаток вычисляется из нового ключа (не вводится).</li>
          <li>Старый ключ уничтожается, операция пишется в Журнал.</li>
        </ol>
        <p class="fpline">текущий отпечаток · <span>{{ showCur ? currentFp : 'скрыт' }}</span>
          <button class="lnk" @click="showCur = !showCur">{{ showCur ? 'скрыть' : 'показать' }}</button></p>

        <label class="field"><span>Текущий ключ vault (для расшифровки)</span>
          <input v-model="cur" type="password" placeholder="ENGRAM_VAULT_KEY — текущее значение" /></label>
        <label class="field"><span>Новый ключ vault</span>
          <input v-model="next" type="password" placeholder="новый мастер-ключ" /></label>
        <label class="field"><span>Повторите новый ключ</span>
          <input v-model="next2" type="password" placeholder="ещё раз, для сверки" /></label>
        <label class="check"><input v-model="ack" type="checkbox" />
          <span>Понимаю, что все {{ t('secrets.nSecrets', secretCount) }} будут перешифрованы, и подтверждаю наличие резервной копии текущего ключа.</span></label>
      </div>
      <div class="mf">
        <button class="tbtn" @click="emit('close')">Отмена</button>
        <button class="tbtn danger" :disabled="!valid" @click="rotate">Перешифровать и сменить ключ</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay { position:fixed; inset:0; background:rgba(8,12,20,.55); display:flex; align-items:center; justify-content:center; z-index:60; }
.modal { background:var(--surface); border:1px solid var(--border); border-radius:var(--r-lg); width:min(540px,calc(100vw - 32px)); max-height:84vh; overflow:auto; box-shadow:var(--elev-raised); }
.modal h2 { font-size:var(--text-lg); margin:0; padding:20px 20px 8px; }
.mb { padding:0 20px 16px; }
.mf { padding:16px 20px; border-top:1px solid var(--border); display:flex; justify-content:flex-end; gap:8px; }
.warn { display:flex; gap:10px; align-items:flex-start; border:1px solid color-mix(in oklab,var(--state-danger),transparent 55%); background:color-mix(in oklab,var(--state-danger),transparent 92%); border-radius:var(--r-md); padding:11px 13px; margin-bottom:16px; }
.warn .ic { color:var(--state-danger); flex:none; }
.warn p { margin:0; font-size:var(--text-sm); color:var(--fg-2); line-height:1.5; }
.warn b { color:var(--fg); }
.steps { list-style:decimal; margin:0 0 16px; padding-left:20px; }
.steps li { font-size:var(--text-sm); color:var(--fg-2); line-height:1.45; margin-bottom:6px; }
.fpline { font-family:var(--font-mono); font-size:var(--text-xs); color:var(--muted); margin:0 0 14px; }
.lnk { background:transparent; border:0; color:var(--reveal-timer); cursor:pointer; font-size:var(--text-xs); }
.field { display:block; margin-bottom:13px; }
.field span { display:block; font-size:var(--text-sm); font-weight:500; color:var(--fg); margin-bottom:5px; }
.field input { width:100%; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--surface); color:var(--fg); font-family:var(--font-mono); font-size:var(--text-sm); padding:7px 9px; }
.check { display:flex; gap:9px; align-items:flex-start; font-size:var(--text-sm); color:var(--fg-2); line-height:1.45; cursor:pointer; }
.check input { margin-top:2px; flex:none; width:15px; height:15px; accent-color:var(--state-danger); }
.tbtn { font-size:var(--text-sm); padding:7px 13px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg); cursor:pointer; }
.tbtn.danger { background:var(--state-danger); border-color:var(--state-danger); color:#fff; }
.tbtn.danger:disabled { opacity:.42; cursor:not-allowed; }
</style>
