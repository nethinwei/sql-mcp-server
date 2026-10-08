import { createApp } from 'vue'
import { createPinia } from 'pinia'
import urql from '@urql/vue'
import App from './App.vue'
import { router } from './router'
import { client } from './api/client'
import { useWorkspace } from './stores/workspace'
import { i18n } from './i18n'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia).use(i18n).use(router).use(urql, client)

// Persist the workspace on every change so a reload never loses edits.
const workspace = useWorkspace(pinia)
workspace.restore()
workspace.$subscribe(() => workspace.persist())

app.mount('#app')
