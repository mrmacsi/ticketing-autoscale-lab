import { createRouter, createWebHistory, type RouteRecordRaw, type RouterHistory } from 'vue-router';

export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
  { path: '/history', name: 'history', component: () => import('@/views/HistoryView.vue') },
  { path: '/experiments/:id/report', name: 'report', component: () => import('@/views/ReportView.vue'), props: true },
  { path: '/:pathMatch(.*)*', redirect: '/' },
];

export function createAppRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({ history, routes, scrollBehavior: () => ({ top: 0 }) });
}
