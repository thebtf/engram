// Nuxt config — design-fidelity scaffold.
// DEVELOPER: this file is your seam. Tune modules, add the auth guard, point the build
// at the Go-embed pipeline. The design track owns pages/components/tokens, not this file.
export default defineNuxtConfig({
  compatibilityDate: '2026-06-01',
  devtools: { enabled: true },
  modules: ['@nuxt/ui', '@nuxtjs/color-mode', '@nuxtjs/i18n'],
  css: ['~/assets/tokens.css'],
  runtimeConfig: {
    operatorApiTarget: process.env.NUXT_OPERATOR_API_TARGET || 'http://unleashed.lan:37777',
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE || '/api',
      apiDisplayHost: process.env.NUXT_PUBLIC_API_DISPLAY_HOST || '',
    },
  },
  colorMode: {
    classSuffix: '',
    dataValue: 'theme',     // sets <html data-theme="dark|light"> — tokens.css keys off this
    preference: 'dark',     // dark is the base (wall display / second monitor)
    fallback: 'dark',
  },
  // i18n — the localization seam. ru is the contract language (matches the .od mockup);
  // en + zh are the launch translation targets. no_prefix = single SPA, locale lives in a
  // cookie, not the URL (the console is an embedded authenticated app, not crawlable pages).
  // Dictionaries live in i18n/locales/ (the swap point — see i18n/locales/ru.json).
  // <html lang> is driven by the active locale automatically; do not hardcode it here.
  // Launch locales: ru (contract) · en · zh (Simplified Chinese). iso aids <html lang>.
  i18n: {
    strategy: 'no_prefix',
    defaultLocale: 'ru',
    lazy: true,
    langDir: 'locales',
    locales: [
      { code: 'ru', name: 'Русский',  language: 'ru-RU',      file: 'ru.json' },
      { code: 'en', name: 'English',  language: 'en-US',      file: 'en.json' },
      { code: 'zh', name: '中文',      language: 'zh-Hans-CN', file: 'zh.json' },
    ],
    detectBrowserLanguage: {
      useCookie: true,
      cookieKey: 'engram_console_lang',
      redirectOn: 'no prefix',
    },
  },
  app: {
    head: {
      title: 'engram · консоль оператора',
    },
  },
  // No page-transition: DESIGN.md Don't — load into the task, not choreography.
  ssr: false,   // operator console is an authenticated SPA embedded in the Go binary
})
