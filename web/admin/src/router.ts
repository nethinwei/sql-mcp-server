import { createRouter, createWebHistory } from 'vue-router'
import { restoreSession, session } from '@/api/session'

export const router = createRouter({
  history: createWebHistory('/admin/'),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('@/components/AppLayout.vue'),
      children: [
        { path: '', name: 'overview', component: () => import('@/views/OverviewView.vue') },
        { path: 'datasources', name: 'datasources', component: () => import('@/views/DatasourcesView.vue') },
        { path: 'entities', name: 'entities', component: () => import('@/views/EntitiesView.vue') },
        { path: 'entities/:name', name: 'entity', component: () => import('@/views/EntityView.vue'), props: true },
        { path: 'roles', name: 'roles', component: () => import('@/views/RolesView.vue') },
        { path: 'roles/:name', name: 'role', component: () => import('@/views/RoleView.vue'), props: true },
        { path: 'users', name: 'users', component: () => import('@/views/UsersView.vue') },
        { path: 'users/:name', name: 'user', component: () => import('@/views/UserView.vue'), props: true },
        { path: 'simulate', name: 'simulate', component: () => import('@/views/SimulateView.vue') },
        { path: 'review', name: 'review', component: () => import('@/views/ReviewView.vue') },
        { path: 'revisions', name: 'revisions', component: () => import('@/views/RevisionsView.vue') },
        { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
        { path: 'admins', name: 'admins', component: () => import('@/views/AdminsView.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  if (!session.ready) await restoreSession()
  if (to.meta.public) return true
  if (!session.username) return { name: 'login', query: { next: to.fullPath } }
  return true
})
