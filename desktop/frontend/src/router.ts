import { createRouter, createWebHashHistory } from 'vue-router'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: '/',
      redirect: '/overview',
    },
    {
      path: '/overview',
      name: 'overview',
      component: () => import('./views/OverviewView.vue'),
      meta: { title: '概览', eyebrow: '连接状态与用量' },
    },
    {
      path: '/studio',
      name: 'studio',
      component: () => import('./views/ImageStudioView.vue'),
      meta: { title: 'AI 创作', eyebrow: '直接调用站点模型' },
    },
    {
      path: '/connect',
      name: 'connect',
      component: () => import('./views/ConnectView.vue'),
      meta: { title: '客户端配置', eyebrow: '官方站点与 API key' },
    },
    {
      path: '/usage',
      name: 'usage',
      component: () => import('./views/UsageView.vue'),
      meta: { title: '用量', eyebrow: '账户与所选 key' },
    },
    {
      path: '/recharge',
      name: 'recharge',
      component: () => import('./views/RechargeView.vue'),
      meta: { title: '充值', eyebrow: '官方托管支付' },
    },
    {
      path: '/account',
      name: 'account',
      component: () => import('./views/AccountView.vue'),
      meta: { title: '账户与设备', eyebrow: '签到、密钥与授权设备' },
    },
    // Kept as a deep-link for the local Codex/Claude configuration tools;
    // the primary navigation intentionally stays focused on six work areas.
    {
      path: '/settings',
      name: 'settings',
      component: () => import('./views/SettingsView.vue'),
      meta: { title: '客户端配置工具', eyebrow: '本地文件与备份' },
    },
  ],
})

export default router
