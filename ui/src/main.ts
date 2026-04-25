import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './assets/main.css'
import '@fortawesome/fontawesome-free/css/all.min.css'
import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'

const app = createApp(App)
app.use(router)
app.mount('#app')
