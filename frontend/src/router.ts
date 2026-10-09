import { createRouter, createWebHistory, RouterView } from 'vue-router'
import MessagesPage from './messages/MessagesPage.vue'
import TasksPage from './tasks/TasksPage.vue'
import LoginPage from './auth/LoginPage.vue'
import { session, restoreSession } from './auth/session.ts'
import { verification } from './auth/verification.ts'

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
    { path: '/tasks', name: 'tasks', component: TasksPage },
    { path: '/tasks/teams/:teamId/:taskId', name: 'task-detail', component: TasksPage },
    { path: '/:pathMatch(.*)*', redirect: '/messages' },
  ],
})
router.beforeEach(async to => {
  if (to.path === '/login') return true
  if (!session.token()) return '/login'
  const result = await verification.check(to.fullPath)
  if (result === 'login') return '/login'
  if (result === 'stale') return false
  return true
})
session.subscribe(() => { if (!session.token()) void router.replace('/login') })
