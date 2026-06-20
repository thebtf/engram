<script setup lang="ts">
/** Rules — always-inject behavioural directives. Seam: → GET/POST/PATCH/DELETE /api/rules. */
import { ref } from 'vue'
import { useRules } from '../composables/useMockData'

const { t } = useI18n()
const rules = useRules()
const content = ref('')
const priority = ref(10)
const creating = ref(false)
const createError = ref<string | null>(null)
const removingId = ref<number | null>(null)

async function createRule() {
  const nextContent = content.value.trim()
  if (!nextContent) {
    createError.value = t('rules.errors.contentRequired')
    return
  }

  creating.value = true
  createError.value = null
  try {
    await rules.create({
      content: nextContent,
      priority: priority.value,
      editedBy: 'operator-console',
    })
    content.value = ''
    priority.value = 10
  } catch (error) {
    createError.value = error instanceof Error ? error.message : String(error)
  } finally {
    creating.value = false
  }
}

async function removeRule(id: number) {
  removingId.value = id
  try {
    await rules.remove(id)
  } finally {
    removingId.value = null
  }
}
</script>

<template>
  <div>
    <header class="head">
      <h1>{{ t('rules.title') }}</h1>
      <p>{{ t('rules.subtitle') }}</p>
    </header>

    <section class="composer">
      <div class="composer-head">
        <h3>{{ t('rules.quickAddTitle') }}</h3>
        <HonestyBadge cls="live" evidence="GET/POST /api/rules" />
      </div>
      <textarea
        v-model="content"
        class="txt"
        rows="3"
        :placeholder="t('rules.quickAddPlaceholder')"
      />
      <div class="composer-controls">
        <label class="pri">
          <span>{{ t('rules.priorityLabel') }}</span>
          <input v-model.number="priority" type="number" min="-100" max="100" />
        </label>
        <button class="act" :disabled="creating" @click="createRule">
          {{ creating ? t('rules.actions.creating') : t('rules.actions.create') }}
        </button>
      </div>
      <p v-if="createError" class="msg err">{{ createError }}</p>
      <p class="msg note">{{ t('rules.quickAddNote') }}</p>
    </section>

    <p v-if="rules.error" class="msg err">{{ rules.error }}</p>
    <p v-else-if="rules.pending && !rules.rows.length" class="msg note">{{ t('rules.loading') }}</p>
    <p v-else-if="!rules.rows.length" class="msg note">{{ t('rules.empty') }}</p>

    <div v-else class="grid">
      <EntityRow
        v-for="rule in rules.rows"
        :key="rule.id"
        :preview="rule.content"
        :meta="[
          rule.project === 'global' ? t('rules.scopeGlobal') : rule.project,
          t('rules.priorityMeta', { n: rule.priority }),
          t('rules.updatedMeta', { value: rule.updated }),
        ]"
        status="live"
      >
        <template #side>
          <HonestyBadge cls="live" />
          <button
            class="ghost"
            :disabled="removingId === rule.id"
            @click="removeRule(rule.id)"
          >
            {{ removingId === rule.id ? t('rules.actions.deleting') : t('rules.actions.delete') }}
          </button>
        </template>
      </EntityRow>
    </div>
  </div>
</template>

<style scoped>
.head h1 { margin:0 0 4px; font-size:var(--text-xl); font-weight:700; }
.head p { margin:0 0 16px; font-size:var(--text-sm); color:var(--muted); }
.composer { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); padding:16px 20px; margin-bottom:14px; }
.composer-head { display:flex; align-items:center; gap:10px; justify-content:space-between; margin-bottom:12px; }
.composer-head h3 { margin:0; font-size:var(--text-sm); font-weight:700; }
.txt { width:100%; resize:vertical; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:10px 12px; font:inherit; }
.composer-controls { display:flex; align-items:end; gap:12px; margin-top:12px; }
.pri { display:flex; flex-direction:column; gap:5px; font-size:var(--text-xs); color:var(--muted); }
.pri input { width:88px; border:1px solid var(--border); border-radius:var(--r-sm); background:var(--bg); color:var(--fg); padding:6px 8px; font:inherit; }
.act, .ghost { font-size:var(--text-sm); padding:7px 12px; border:1px solid var(--border); border-radius:var(--r-sm); background:transparent; color:var(--fg); cursor:pointer; }
.act:hover, .ghost:hover { background:var(--surface-warm); }
.act:disabled, .ghost:disabled { opacity:.6; cursor:not-allowed; }
.msg { margin:10px 0 0; font-size:var(--text-sm); }
.msg.note { color:var(--muted); }
.msg.err { color:var(--state-danger); }
.grid { border:1px solid var(--border); border-radius:var(--r-md); background:var(--surface); overflow:hidden; }
</style>
