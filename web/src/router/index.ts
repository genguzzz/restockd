// 路由：history 模式；未登录访问任意页跳登录
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { setUnauthorizedHandler } from '@/api/client'

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('@/pages/LoginPage.vue'), meta: { public: true } },
  {
    path: '/',
    component: () => import('@/layouts/MainLayout.vue'),
    children: [
      { path: '', name: 'dashboard', component: () => import('@/pages/DashboardPage.vue'), meta: { title: '概览' } },
      { path: 'products', name: 'products', component: () => import('@/pages/ProductsPage.vue'), meta: { title: '商品' } },
      { path: 'watches', name: 'watches', component: () => import('@/pages/WatchesPage.vue'), meta: { title: '关注与锁单' } },
      { path: 'accounts', name: 'accounts', component: () => import('@/pages/AccountsPage.vue'), meta: { title: '账号' } },
      { path: 'orders', name: 'orders', component: () => import('@/pages/OrdersPage.vue'), meta: { title: '订单' } },
      { path: 'challenges', name: 'challenges', component: () => import('@/pages/ChallengesPage.vue'), meta: { title: '人工验证' } },
      { path: 'sites', name: 'sites', component: () => import('@/pages/SitesPage.vue'), meta: { title: '站点' } },
      { path: 'settings', name: 'settings', component: () => import('@/pages/SettingsPage.vue'), meta: { title: '设置' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({ history: createWebHistory(), routes })

setUnauthorizedHandler(() => {
  if (router.currentRoute.value.name !== 'login') {
    void router.push({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
  }
})

router.afterEach((to) => {
  const title = to.meta.title as string | undefined
  document.title = title ? `${title} · restockd` : 'restockd'
})
