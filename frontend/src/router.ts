import { h } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'

const placeholder = (title: string) => ({
  render: () => h('section', { class: 'placeholder-page' }, [
    h('h1', title),
    h('p', '页面框架已就绪，内容将在下一步接入。'),
  ]),
})

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/messages' },
    { path: '/messages', component: placeholder('消息') },
    { path: '/tasks', component: placeholder('我的任务') },
  ],
})
