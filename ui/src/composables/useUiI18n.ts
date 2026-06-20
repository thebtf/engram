import { computed, watch } from 'vue'
import { i18n, UI_LOCALE_STORAGE_KEY } from '@/i18n'
import { messages } from '@/i18n/messages'
import type { SupportedLocale } from '@/i18n/messages'

let watchStarted = false

export function useUiI18n() {
  const globalLocale = i18n.global.locale as unknown as { value: SupportedLocale }
  const locale = computed(() => globalLocale.value)

  function setLocale(next: SupportedLocale) {
    globalLocale.value = next
  }

  if (!watchStarted) {
    watchStarted = true
    watch(
      () => globalLocale.value,
      value => {
        localStorage.setItem(UI_LOCALE_STORAGE_KEY, value)
        document.documentElement.lang = value
      },
      { immediate: true },
    )
  }

  return {
    locale,
    t: computed(() => messages[locale.value]),
    setLocale,
    availableLocales: ['ru', 'en'] as const,
  }
}
