<script setup lang="ts">
import { ref, watch } from 'vue'

const props = defineProps<{
  show: boolean
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()

const cancelButtonRef = ref<HTMLButtonElement | null>(null)

// Focus the cancel button when the dialog opens for keyboard accessibility.
watch(() => props.show, (visible) => {
  if (visible) {
    // Defer to next tick so the DOM is rendered.
    setTimeout(() => cancelButtonRef.value?.focus(), 0)
  }
})

function handleKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    emit('cancel')
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <!-- Backdrop -->
        <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="emit('cancel')" />

        <!-- Dialog -->
        <div
          role="dialog"
          aria-modal="true"
          :aria-labelledby="'confirm-dialog-title'"
          class="relative glass border border-white/10 rounded-xl p-6 max-w-sm w-full shadow-2xl"
          @keydown="handleKeydown"
        >
          <h3 id="confirm-dialog-title" class="text-lg font-semibold text-white mb-2">{{ title }}</h3>
          <p class="text-sm text-slate-400 mb-6">{{ message }}</p>

          <div class="flex items-center justify-end gap-3">
            <button
              ref="cancelButtonRef"
              class="px-4 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800/50 transition-colors"
              @click="emit('cancel')"
            >
              {{ cancelLabel ?? 'Cancel' }}
            </button>
            <button
              :class="[
                'px-4 py-2 rounded-lg text-sm font-medium transition-colors',
                danger
                  ? 'bg-red-500/20 text-red-400 hover:bg-red-500/30 border border-red-500/50'
                  : 'bg-claude-500 text-white hover:bg-claude-400',
              ]"
              @click="emit('confirm')"
            >
              {{ confirmLabel ?? 'Confirm' }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
