<template>
  <main class="login-container">
    <el-card class="login-card" aria-labelledby="login-title">
      <template #header>
        <div class="card-header">
          <div class="brand-mark" aria-hidden="true">G</div>
          <h2 id="login-title">登录</h2>
          <p class="card-subtitle">继续使用 GopherAI 智能应用平台</p>
        </div>
      </template>
      <el-form
        ref="loginFormRef"
        :model="loginForm"
        :rules="loginRules"
        label-width="80px"
      >
        <el-form-item label="用户名" prop="username">
          <el-input
            v-model="loginForm.username"
            placeholder="请输入用户名"
            name="username"
            autocomplete="username"
          />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="loginForm.password"
            placeholder="请输入密码"
            type="password"
            show-password
            name="password"
            autocomplete="current-password"
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            native-type="button"
            :loading="loading"
            class="submit-btn"
            @click="handleLogin"
          >
            登录
          </el-button>
        </el-form-item>
        <el-form-item>
          <el-button
            link
            native-type="button"
            class="switch-btn"
            @click="$router.push('/register')"
          >
            还没有账号？去注册
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </main>
</template>

<script>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import api, { markSessionAuthenticated } from '../utils/api'
import { ElButton, ElCard, ElForm, ElFormItem, ElInput } from '../utils/elementAuth'
import { ElMessage } from '../utils/elementFeedback'

export default {
  name: 'LoginView',
  components: {
    ElButton,
    ElCard,
    ElForm,
    ElFormItem,
    ElInput
  },
  setup() {
    const router = useRouter()
    const loginFormRef = ref()
    const loading = ref(false)
    const loginForm = ref({
      username: '',
      password: ''
    })

    const loginRules = {
      username: [
        { required: true, message: '请输入用户名', trigger: 'blur' }
      ],
      password: [
        { required: true, message: '请输入密码', trigger: 'blur' },
        { min: 6, message: '密码长度不能少于6位', trigger: 'blur' }
      ]
    }

    const handleLogin = async () => {
      try {
        await loginFormRef.value.validate()
        loading.value = true
        const response = await api.post('/user/login', {
          username: loginForm.value.username,
          password: loginForm.value.password
        })
        if (response.data.status_code === 1000) {
          markSessionAuthenticated()
          ElMessage.success('登录成功')
          const redirect = router.currentRoute.value.query.redirect
          const destination = typeof redirect === 'string' && redirect.startsWith('/') && !redirect.startsWith('//')
            ? redirect
            : '/menu'
          router.replace(destination)
        } else {
          ElMessage.error(response.data.status_msg || '登录失败')
        }
      } catch (error) {
        console.error('Login error:', error)
        ElMessage.error(error.response?.data?.status_msg || '登录失败，请重试')
      } finally {
        loading.value = false
      }
    }

    return {
      loginFormRef,
      loading,
      loginForm,
      loginRules,
      handleLogin
    }
  }
}
</script>

<style scoped>
.login-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  padding: 24px;
  background: var(--mac-bg);
}

/*
 * A wide, very faint light source above the card. It is pure greyscale, so it
 * adds depth to the canvas without introducing a tint.
 */
.login-container::before {
  content: '';
  position: fixed;
  inset: 0;
  pointer-events: none;
  background: radial-gradient(120% 80% at 50% -10%, rgba(255, 255, 255, 0.9), transparent 60%);
}

.login-card {
  position: relative;
  width: min(400px, 100%);
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-xl);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-3);
}

.login-card :deep(.el-card__header) {
  padding: 32px 32px 20px;
  border-bottom: 0;
}

.login-card :deep(.el-card__body) {
  padding: 0 32px 28px;
}

.card-header {
  text-align: center;
}

/* Rounded-square app mark, the shape macOS uses for application icons. */
.brand-mark {
  display: grid;
  place-items: center;
  width: 52px;
  height: 52px;
  margin: 0 auto 16px;
  border-radius: var(--mac-radius-lg);
  color: var(--mac-white);
  background: var(--mac-accent-bg);
  box-shadow: var(--mac-accent-ring);
  font-size: 26px;
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
  user-select: none;
}

.card-header h2 {
  margin: 0;
  font-size: var(--mac-text-2xl);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tighter);
}

.card-subtitle {
  margin: 6px 0 0;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

.login-card :deep(.el-form-item) {
  margin-bottom: 18px;
}

.login-card :deep(.el-form-item__label) {
  font-size: var(--mac-text-base);
}

.submit-btn {
  width: 100%;
  min-height: 40px;
}

.switch-btn {
  width: 100%;
  color: var(--mac-text-secondary);
  font-weight: 500;
}

.switch-btn:hover {
  color: var(--mac-text);
}

.login-card :deep(.el-form-item:last-child) {
  margin-bottom: 0;
}

@media (max-width: 420px) {
  .login-container {
    padding: 14px;
  }

  .login-card :deep(.el-card__header) {
    padding: 26px 20px 16px;
  }

  .login-card :deep(.el-card__body) {
    padding: 0 20px 22px;
  }

  .card-header h2 {
    font-size: var(--mac-text-xl);
  }

  .login-card :deep(.el-form-item__label) {
    width: 68px !important;
  }

  .login-card :deep(.el-form-item__content) {
    margin-left: 68px !important;
  }
}
</style>
