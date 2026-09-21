import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import DashboardView from '@/views/DashboardView.vue'

/** 两个路由：看板（/dashboard/:id）与信任指引（/trust）。 */
const routes: RouteRecordRaw[] = [
  {
    path: '/',
    redirect: () => ({ name: 'dashboard', params: { id: '' } }),
  },
  {
    path: '/dashboard/:id?',
    name: 'dashboard',
    component: DashboardView,
    props: true,
  },
  {
    path: '/trust',
    name: 'trust',
    component: () => import('@/views/TrustGuideView.vue'),
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: () => ({ name: 'dashboard', params: { id: '' } }),
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
