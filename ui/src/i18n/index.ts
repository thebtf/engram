import { createI18n } from 'vue-i18n'
import { messages, type SupportedLocale } from './messages'

const STORAGE_KEY = 'engram:ui-locale'

function resolveInitialLocale(): SupportedLocale {
  if (typeof window !== 'undefined') {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'ru' || stored === 'en') {
      return stored
    }
  }

  return 'ru'
}

export const i18n = createI18n({
  legacy: false,
  locale: resolveInitialLocale(),
  fallbackLocale: 'en',
  messages: messages as Record<string, any>,
})

export { STORAGE_KEY as UI_LOCALE_STORAGE_KEY }
