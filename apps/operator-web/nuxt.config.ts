export default defineNuxtConfig({
  compatibilityDate: '2026-06-20',
  ssr: false,
  nitro: {
    preset: 'node-server',
  },
  devtools: { enabled: true },
  css: ['./app/assets/css/main.css'],
  postcss: {
    plugins: {
      tailwindcss: {},
    },
  },
  app: {
    head: {
      title: 'engram operator web',
      meta: [
        {
          name: 'viewport',
          content: 'width=device-width, initial-scale=1',
        },
      ],
    },
  },
  runtimeConfig: {
    engramApiTarget: 'http://127.0.0.1:37777',
    public: {
      apiBase: '/api',
    },
  },
  typescript: {
    strict: true,
    typeCheck: false,
  },
})
