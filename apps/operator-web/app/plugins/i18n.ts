import { createI18n } from 'vue-i18n'
import { watch } from 'vue'
import { messages as sharedMessages } from '../../../../ui/src/i18n/messages'

const messages = {
  ru: {
    projectCatalog: sharedMessages.ru.projectCatalog,
    common: {
      loading: 'Загрузка...',
      empty: 'Пусто',
      allProjects: 'Все проекты',
      refresh: 'Обновить',
      none: '—',
    },
    rules: {
      scope: 'Область правил',
      globalOnly: 'Только глобальные правила',
      projectAndGlobal: 'Правила выбранного проекта и глобальные правила',
      loadedRows: 'загружено строк',
      loadedRules: 'Загруженные правила',
    },
  },
  en: {
    projectCatalog: sharedMessages.en.projectCatalog,
    common: {
      loading: 'Loading...',
      empty: 'Empty',
      allProjects: 'All projects',
      refresh: 'Refresh',
      none: '—',
    },
    rules: {
      scope: 'Rules scope',
      globalOnly: 'Global rules only',
      projectAndGlobal: 'Selected project and global rules',
      loadedRows: 'rows loaded',
      loadedRules: 'Loaded rules',
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
