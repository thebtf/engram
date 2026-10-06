import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'

export default defineConfig(({ mode }) => {
  const env = { ...loadEnv(mode, process.cwd(), ''), ...process.env }
  const apiTarget = env.ENGRAM_OPERATOR_API_TARGET || 'http://127.0.0.1:37777'
  return {
    plugins: [
      vue(),
      AutoImport({ imports: ['vue', 'vue-router', 'vue-i18n'], dirs: ['./composables'], dts: './.output/auto-imports.d.ts', vueTemplate: true }),
      Components({ dirs: ['./components'], directoryAsNamespace: false, dts: './.output/components.d.ts' }),
    ],
    resolve: { alias: { '~': fileURLToPath(new URL('.', import.meta.url)) } },
    define: {
      __VUE_I18N_FULL_INSTALL__: true,
      __VUE_I18N_LEGACY_API__: false,
      __INTLIFY_PROD_DEVTOOLS__: false,
    },
    build: { outDir: '.output/public', assetsDir: '_nuxt', emptyOutDir: true },
    server: { proxy: { '/api': { target: apiTarget, changeOrigin: false } } },
  }
})
