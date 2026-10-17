import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { resolveBuildPaths } from './scripts/build-paths.mjs'

export default defineConfig(({ mode }) => {
  const env = { ...loadEnv(mode, process.cwd(), ''), ...process.env }
  const apiTarget = env.ENGRAM_OPERATOR_API_TARGET || 'http://127.0.0.1:37777'
  const paths = resolveBuildPaths(process.env.ENGRAM_OPERATOR_OUTPUT_ROOT)
  return {
    ...(paths.overridden ? { publicDir: paths.publicDir, cacheDir: paths.cacheDir } : {}),
    plugins: [
      vue(),
      AutoImport({ imports: ['vue', 'vue-router', 'vue-i18n'], dirs: ['./composables'], dts: join(paths.outputRoot, 'auto-imports.d.ts'), vueTemplate: true }),
      Components({ dirs: ['./components'], directoryAsNamespace: false, dts: join(paths.outputRoot, 'components.d.ts') }),
    ],
    resolve: { alias: { '~': fileURLToPath(new URL('.', import.meta.url)) } },
    define: {
      __VUE_I18N_FULL_INSTALL__: true,
      __VUE_I18N_LEGACY_API__: false,
      __INTLIFY_PROD_DEVTOOLS__: false,
    },
    build: { outDir: paths.outputPublicDir, assetsDir: '_nuxt', emptyOutDir: true },
    server: { proxy: { '/api': { target: apiTarget, changeOrigin: false } } },
  }
})
