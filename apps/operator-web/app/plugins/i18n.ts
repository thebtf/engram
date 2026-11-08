import { createI18n } from 'vue-i18n'
import { watch } from 'vue'
import { messages as sharedMessages, issueFlowZh, projectCatalogZh } from '../../../../ui/src/i18n/messages'

const messages = {
  ru: {
    issueFlow: sharedMessages.ru.issueFlow,
    projectCatalog: sharedMessages.ru.projectCatalog,
    common: {
      loading: 'Загрузка...',
      empty: 'Пусто',
      allProjects: 'Все проекты',
      refresh: 'Обновить',
      none: '—',
    },
  },
  en: {
    issueFlow: sharedMessages.en.issueFlow,
    projectCatalog: sharedMessages.en.projectCatalog,
    common: {
      loading: 'Loading...',
      empty: 'Empty',
      allProjects: 'All projects',
      refresh: 'Refresh',
      none: '—',
    },
  },
  zh: {
    issueFlow: issueFlowZh,
    projectCatalog: projectCatalogZh,
    common: { loading: '加载中…', empty: '空', allProjects: '所有项目', refresh: '刷新', none: '—' },
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
