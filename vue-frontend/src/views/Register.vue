<template>
  <main class="register-container">
    <el-card class="register-card" aria-labelledby="register-title">
      <template #header>
        <div class="card-header">
          <div class="brand-mark" aria-hidden="true">G</div>
          <h2 id="register-title">注册</h2>
          <p class="card-subtitle">创建你的 GopherAI 账号</p>
        </div>
      </template>
      <el-form
        ref="registerFormRef"
        :model="registerForm"
        :rules="registerRules"
        label-width="80px"
      >
        <el-form-item label="邮箱" prop="email">
          <el-input
            v-model="registerForm.email"
            placeholder="请输入邮箱"
            type="email"
            name="email"
            autocomplete="email"
          />
        </el-form-item>
        <el-form-item label="验证码" prop="captcha">
          <el-row :gutter="10">
            <el-col :span="16">
              <el-input
                v-model="registerForm.captcha"
                placeholder="请输入验证码"
                name="captcha"
                autocomplete="one-time-code"
              />
            </el-col>
            <el-col :span="8">
              <el-button
                native-type="button"
                class="captcha-btn"
                :loading="codeLoading"
                :disabled="countdown > 0"
                aria-live="polite"
                @click="sendCode"
              >
                {{ countdown > 0 ? `${countdown}s` : '发送验证码' }}
              </el-button>
            </el-col>
          </el-row>
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="registerForm.password"
            placeholder="请输入密码"
            type="password"
            show-password
            name="password"
            autocomplete="new-password"
          />
        </el-form-item>
        <el-form-item label="确认密码" prop="confirmPassword">
          <el-input
            v-model="registerForm.confirmPassword"
            placeholder="请再次输入密码"
            type="password"
            show-password
            name="confirmPassword"
            autocomplete="new-password"
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            native-type="button"
            :loading="loading"
            class="submit-btn"
            @click="handleRegister"
          >
            注册
          </el-button>
        </el-form-item>
        <el-form-item>
          <el-button
            link
            native-type="button"
            class="switch-btn"
            @click="$router.push('/login')"
          >
            已有账号？去登录
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </main>
</template>

<script>
import { ref, reactive } from 'vue'
import { useRouter } from 'vue-router'
import api, { markSessionAuthenticated } from '../utils/api'
import { ElButton, ElCard, ElCol, ElForm, ElFormItem, ElInput, ElRow } from '../utils/elementAuth'
import { ElMessage, ElMessageBox } from '../utils/elementFeedback'

export default {
  name: 'RegisterView',
  components: {
    ElButton,
    ElCard,
    ElCol,
    ElForm,
    ElFormItem,
    ElInput,
    ElRow
  },
  setup() {
    const router = useRouter()
    const registerFormRef = ref()
    const loading = ref(false)
    const codeLoading = ref(false)
    const countdown = ref(0)

    const registerForm = reactive({
      email: '',
      captcha: '',
      password: '',
      confirmPassword: ''
    })

    const validateConfirmPassword = (rule, value, callback) => {
      if (value !== registerForm.password) {
        callback(new Error('两次输入密码不一致'))
      } else {
        callback()
      }
    }

    const registerRules = {
      email: [
        { required: true, message: '请输入邮箱', trigger: 'blur' },
        { type: 'email', message: '请输入正确的邮箱格式', trigger: 'blur' }
      ],
      captcha: [
        { required: true, message: '请输入验证码', trigger: 'blur' }
      ],
      password: [
        { required: true, message: '请输入密码', trigger: 'blur' },
        { min: 6, message: '密码长度不能少于6位', trigger: 'blur' }
      ],
      confirmPassword: [
        { required: true, message: '请确认密码', trigger: 'blur' },
        { validator: validateConfirmPassword, trigger: 'blur' }
      ]
    }

    const sendCode = async () => {
      if (!registerForm.email) {
        ElMessage.warning('请先输入邮箱')
        return
      }
      try {
        codeLoading.value = true
        const response = await api.post('/user/captcha', { email: registerForm.email })
        if (response.data.status_code === 1000) {
          ElMessage.success('验证码发送成功')
          countdown.value = 60
          const timer = setInterval(() => {
            countdown.value--
            if (countdown.value <= 0) {
              clearInterval(timer)
            }
          }, 1000)
        } else {
          ElMessage.error(response.data.status_msg || '验证码发送失败')
        }
      } catch (error) {
        console.error('Send code error:', error)
        ElMessage.error(error.response?.data?.status_msg || '验证码发送失败，请重试')
      } finally {
        codeLoading.value = false
      }
    }

    const handleRegister = async () => {
      try {
        await registerFormRef.value.validate()
        loading.value = true
        const response = await api.post('/user/register', {
              email: registerForm.email,
              captcha: registerForm.captcha,
              password: registerForm.password
        })
        if (response.data.status_code === 1000) {
          const username = response.data.username || ''
          const sessionEstablished = response.data.session_established === true
          if (sessionEstablished) markSessionAuthenticated()
          await ElMessageBox.alert(
            `注册成功。你的登录账号是：${username || '请查看注册邮件'}。请妥善保存。`,
            '请保存登录账号',
            { confirmButtonText: '我已保存', type: 'success' }
          )
          router.replace(sessionEstablished ? '/menu' : '/login')
        } else {
          ElMessage.error(response.data.status_msg || '注册失败')
        }
      } catch (error) {
        console.error('Register error:', error)
        ElMessage.error(error.response?.data?.status_msg || '注册失败，请重试')
      } finally {
        loading.value = false
      }
    }

    return {
      registerFormRef,
      loading,
      codeLoading,
      countdown,
      registerForm,
      registerRules,
      sendCode,
      handleRegister
    }
  }
}
</script>

<style scoped>
.register-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  padding: 24px;
  background: var(--mac-bg);
}

/*
 * A wide, very faint light source above the card. Pure greyscale, so it adds
 * depth to the canvas without introducing a tint.
 */
.register-container::before {
  content: '';
  position: fixed;
  inset: 0;
  pointer-events: none;
  background: radial-gradient(120% 80% at 50% -10%, rgba(255, 255, 255, 0.9), transparent 60%);
}

.register-card {
  position: relative;
  width: min(430px, 100%);
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-xl);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-3);
}

.register-card :deep(.el-card__header) {
  padding: 32px 32px 20px;
  border-bottom: 0;
}

.register-card :deep(.el-card__body) {
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

.register-card :deep(.el-form-item) {
  margin-bottom: 18px;
}

.register-card :deep(.el-form-item__label) {
  font-size: var(--mac-text-base);
}

/*
 * The captcha action is deliberately a secondary control: the solid black
 * accent stays reserved for the one primary action on the form.
 */
.captcha-btn {
  width: 100%;
  padding-inline: 8px;
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

.register-card :deep(.el-form-item:last-child) {
  margin-bottom: 0;
}

@media (max-width: 420px) {
  .register-container {
    padding: 12px;
  }

  .register-card :deep(.el-card__header) {
    padding: 26px 20px 16px;
  }

  .register-card :deep(.el-card__body) {
    padding: 0 20px 22px;
  }

  .card-header h2 {
    font-size: var(--mac-text-xl);
  }

  .register-card :deep(.el-form-item) {
    margin-bottom: 15px;
  }

  .register-card :deep(.el-form-item__label) {
    width: 68px !important;
  }

  .register-card :deep(.el-form-item__content) {
    margin-left: 68px !important;
  }

  .register-card :deep(.el-row) {
    flex-wrap: nowrap;
  }

  .captcha-btn {
    font-size: var(--mac-text-sm);
  }
}
</style>
