import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './app.vue'
import { i18n } from './i18n/i18n.config'
import { installChunkRecovery } from './plugins/chunk-reload.client'
import './assets/tokens.css'
import './assets/base.css'

const pages = import.meta.glob('./pages/*.vue')
const router = createRouter({
  history: createWebHistory(),
  routes: Object.entries(pages).map(([file, component]) => {
    const name = file.slice('./pages/'.length, -'.vue'.length)
    return { name, path: name === 'index' ? '/' : `/${name}`, component }
  }),
})
installChunkRecovery(router)
createApp(App).use(router).use(i18n).mount('#app')
