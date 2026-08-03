<template>
  <main class="not-found-container">
    <section class="not-found-card" aria-labelledby="not-found-title">
      <p class="error-code" aria-hidden="true">404</p>
      <h1 id="not-found-title">页面走丢了</h1>
      <p>你访问的页面不存在或已经移动。</p>
      <button type="button" @click="goHome">返回首页</button>
    </section>
  </main>
</template>

<script>
import { useRouter } from 'vue-router'
import { ensureAuthenticated } from '../utils/api'

export default {
  name: 'NotFound',
  setup() {
    const router = useRouter()
    const goHome = async () => {
      router.push(await ensureAuthenticated() ? '/menu' : '/login')
    }

    return { goHome }
  }
}
</script>

<style scoped>
.not-found-container {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 24px;
  background: var(--mac-bg);
}

.not-found-card {
  width: min(440px, 100%);
  padding: 48px 32px;
  text-align: center;
  color: var(--mac-text);
  background: var(--mac-surface);
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-xl);
  box-shadow: var(--mac-elev-3);
}

.error-code {
  margin: 0;
  font-size: clamp(64px, 20vw, 108px);
  font-weight: 700;
  line-height: 1;
  letter-spacing: var(--mac-tracking-tighter);
  /* Sits behind the message rather than competing with it. */
  color: var(--mac-text-quaternary);
}

h1 {
  margin: 18px 0 10px;
  font-size: var(--mac-text-xl);
}

.not-found-card > p:not(.error-code) {
  margin-bottom: 28px;
  color: var(--mac-text-secondary);
}

button {
  min-height: 36px;
  padding: 0 20px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-white);
  font-weight: 590;
  letter-spacing: -0.01em;
  background: var(--mac-accent-bg);
  box-shadow: var(--mac-accent-ring);
  transition: background var(--mac-dur-fast) var(--mac-ease);
}

button:hover {
  background: var(--mac-accent-bg-hover);
}

button:active {
  background: var(--mac-black-active);
  box-shadow: inset 0 1px 3px rgba(0, 0, 0, 0.4);
}
</style>
