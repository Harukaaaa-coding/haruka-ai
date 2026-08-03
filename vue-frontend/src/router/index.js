import { createRouter, createWebHistory } from 'vue-router'
import { ensureAuthenticated } from '../utils/api'

const Login = () => import(/* webpackChunkName: "auth" */ '../views/Login.vue')
const Register = () => import(/* webpackChunkName: "auth" */ '../views/Register.vue')
const Menu = () => import(/* webpackChunkName: "menu" */ '../views/Menu.vue')
const AIChat = () => import(/* webpackChunkName: "ai-chat" */ '../views/AIChat.vue')
const ImageRecognition = () => import(/* webpackChunkName: "image-recognition" */ '../views/ImageRecognition.vue')
const KnowledgeBase = () => import(/* webpackChunkName: "knowledge-base" */ '../views/KnowledgeBase.vue')
const MCPHub = () => import(/* webpackChunkName: "mcp-hub" */ '../views/MCPHub.vue')
const ModelCatalog = () => import(/* webpackChunkName: "model-catalog" */ '../views/ModelCatalog.vue')
const AgentTasks = () => import(/* webpackChunkName: "agent-tasks" */ '../views/AgentTasks.vue')
const NotFound = () => import(/* webpackChunkName: "not-found" */ '../views/NotFound.vue')

const routes = [
  {
    path: '/',
    redirect: '/login'
  },
  {
    path: '/login',
    name: 'Login',
    component: Login
  },
  {
    path: '/register',
    name: 'Register',
    component: Register
  },
  {
    path: '/menu',
    name: 'Menu',
    component: Menu,
    meta: { requiresAuth: true }
  },
  {
    path: '/ai-chat',
    name: 'AIChat',
    component: AIChat,
    meta: { requiresAuth: true }
  },
  {
    path: '/image-recognition',
    name: 'ImageRecognition',
    component: ImageRecognition,
    meta: { requiresAuth: true }
  },
  {
    path: '/knowledge-bases',
    name: 'KnowledgeBase',
    component: KnowledgeBase,
    meta: { requiresAuth: true }
  },
  {
    path: '/mcp-hub',
    name: 'MCPHub',
    component: MCPHub,
    meta: { requiresAuth: true }
  },
  {
    path: '/models',
    name: 'ModelCatalog',
    component: ModelCatalog,
    meta: { requiresAuth: true }
  },
  {
    path: '/agent-tasks/:taskId?',
    name: 'AgentTasks',
    component: AgentTasks,
    meta: { requiresAuth: true }
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: NotFound
  }
]

const router = createRouter({
  history: createWebHistory(process.env.BASE_URL),
  routes
})

const safeRedirect = redirect => {
  if (typeof redirect !== 'string' || !redirect.startsWith('/') || redirect.startsWith('//')) {
    return null
  }
  return redirect
}

router.beforeEach(async to => {
  if (to.matched.some(record => record.meta.requiresAuth)) {
    if (!(await ensureAuthenticated())) {
      return {
        name: 'Login',
        query: { redirect: to.fullPath }
      }
    }
  }

  if (to.name === 'Login' && await ensureAuthenticated()) {
    return safeRedirect(to.query.redirect) || { name: 'Menu' }
  }

  return true
})

export default router
