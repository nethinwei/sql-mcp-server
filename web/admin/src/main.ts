import { createApp } from 'vue'
import { createPinia } from 'pinia'
import urql from '@urql/vue'
import App from './App.vue'
import { router } from './router'
import { client } from './api/client'
import { i18n } from './i18n'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia).use(i18n).use(router).use(urql, client)

app.mount('#app')
