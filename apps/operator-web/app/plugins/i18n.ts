import { createI18n } from 'vue-i18n'
import { watch } from 'vue'

const messages = {
  ru: {
    common: {
      loading: 'Загрузка...',
      empty: 'Пусто',
      allProjects: 'Все проекты',
      refresh: 'Обновить',
      none: '—',
    },
  },
  en: {
    common: {
      loading: 'Loading...',
      empty: 'Empty',
      allProjects: 'All projects',
      refresh: 'Refresh',
      none: '—',
    },
  },
}

export default defineNuxtPlugin((nuxtApp) => {
  const initialLocale = import.meta.client
    ? localStorage.getItem('engram.operator.locale') || 'ru'
    : 'ru'

  const i18n = createI18n({
    legacy: false,
    locale: initialLocale,
    fallbackLocale: 'en',
    messages,
  })

  nuxtApp.vueApp.use(i18n)

  if (import.meta.client) {
    watch(i18n.global.locale, (nextLocale) => {
      localStorage.setItem('engram.operator.locale', String(nextLocale))
    })
  }
})
