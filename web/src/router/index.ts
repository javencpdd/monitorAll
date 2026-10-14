import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import DashboardView from '@/views/DashboardView.vue'
import MapBoardView from '@/views/MapBoardView.vue'

/** 三个路由：网格看板（/dashboard/:id）、地图看板（/mapboard/:id）、信任指引（/trust）。 */
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
    // [v3] 地图看板。与网格看板并列的独立实体，不是同一看板的视图切换。
    // kind 不匹配时由 MapBoardView 就地纠正回 /dashboard（前端守卫，后端 40003 只是兜底）。
    path: '/mapboard/:id',
    name: 'mapboard',
    component: MapBoardView,
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
