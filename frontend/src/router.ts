import { createRouter, createWebHistory, RouterView } from 'vue-router'
import MessagesPage from './messages/MessagesPage.vue'
import TaskPreview from './TaskPreview.vue'
import LoginPage from './auth/LoginPage.vue'
import { session, restoreSession } from './auth/session.ts'
import { verifySession, StaleRequestError } from './api/client.ts'

restoreSession()
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/messages' },
    { path: '/login', component: LoginPage },
    {
      path: '/messages', component: RouterView, children: [
        { path: '', name: 'overview', component: MessagesPage },
        { path: 'direct/:peerId', name: 'direct', component: MessagesPage },
        { path: 'teams/:teamId/groups/:groupId', name: 'group', component: MessagesPage },
      ],
    },
    { path: '/tasks', component: TaskPreview },
    { path: '/:pathMatch(.*)*', redirect: '/messages' },
  ],
})
router.beforeEach(async to => {
  if (to.path === '/login') return true
  if (!session.token()) return '/login'
  try { await verifySession(); return true }
  catch (error) { if (error instanceof StaleRequestError) return false; return '/login' }
})
session.subscribe(() => { if (!session.token()) void router.replace('/login') })
