import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './assets/main.css'
import '@fortawesome/fontawesome-free/css/all.min.css'
import '@fontsource/fira-sans/400.css'
import '@fontsource/fira-sans/500.css'
import '@fontsource/fira-sans/600.css'
import '@fontsource/fira-sans/700.css'
import '@fontsource/fira-code/400.css'
import '@fontsource/fira-code/500.css'

const app = createApp(App)
app.use(router)
app.mount('#app')
