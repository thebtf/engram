import type { Config } from 'tailwindcss'

export default {
  content: [
    './app/**/*.{js,ts,vue}',
    './nuxt.config.ts',
  ],
  theme: {
    extend: {},
  },
  plugins: [],
} satisfies Config
