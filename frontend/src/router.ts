import { createRouter, createWebHistory, RouterView } from 'vue-router'
import MessagesPage from './messages/MessagesPage.vue'
import TaskPreview from './TaskPreview.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/messages' },
    {
      path: '/messages', component: RouterView, children: [
        { path: '', name: 'overview', component: MessagesPage },
        { path: 'direct/:peerId', name: 'direct', component: MessagesPage },
        { path: 'teams/:teamId/groups/:groupId', name: 'group', component: MessagesPage },
      ],
    },
    { path: '/tasks', component: TaskPreview },
  ],
})
