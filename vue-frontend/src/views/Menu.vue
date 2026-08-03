<template>
  <div class="menu-container">
    <header class="toolbar">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true">G</span>
        <h1>AI应用平台</h1>
      </div>
      <button type="button" class="ghost-btn" @click="handleLogout">退出登录</button>
    </header>

    <main class="main">
      <div class="page-intro">
        <h2>你好</h2>
        <p>选择一个功能开始，全部能力共享同一套模型与凭据配置。</p>
      </div>

      <div class="menu-grid">
        <button
          v-for="tile in tiles"
          :key="tile.path"
          type="button"
          class="menu-item"
          :aria-label="tile.ariaLabel"
          @click="$router.push(tile.path)"
        >
          <span class="tile-glyph" aria-hidden="true">
            <component :is="tile.icon" />
          </span>
          <span class="tile-body">
            <span class="tile-title">{{ tile.title }}</span>
            <span class="tile-desc">{{ tile.desc }}</span>
          </span>
          <span class="tile-chevron" aria-hidden="true">›</span>
        </button>
      </div>
    </main>
  </div>
</template>

<script>
import { markRaw } from 'vue'
import { useRouter } from 'vue-router'
import { logout } from '../utils/api'
import { ChatDotRound, Camera, Collection, Connection, Cpu, Operation } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from '../utils/elementFeedback'

/*
 * Rendered with a v-for rather than six near-identical blocks. markRaw keeps the
 * icon components out of Vue's reactivity system — they are static.
 */
const tiles = [
  {
    path: '/ai-chat',
    icon: markRaw(ChatDotRound),
    title: 'AI聊天',
    desc: '与AI进行智能对话',
    ariaLabel: '进入 AI 聊天'
  },
  {
    path: '/models',
    icon: markRaw(Cpu),
    title: '模型目录',
    desc: '查看供应商、模型与能力状态',
    ariaLabel: '进入模型目录'
  },
  {
    path: '/agent-tasks',
    icon: markRaw(Operation),
    title: 'Agent 任务',
    desc: '任务规划、工具审批与中断恢复',
    ariaLabel: '进入 Agent 任务'
  },
  {
    path: '/mcp-hub',
    icon: markRaw(Connection),
    title: 'MCP Hub',
    desc: '工具发现、调用审批与审计',
    ariaLabel: '进入 MCP Hub'
  },
  {
    path: '/knowledge-bases',
    icon: markRaw(Collection),
    title: '知识库',
    desc: '多文档索引、检索与引用管理',
    ariaLabel: '进入知识库管理'
  },
  {
    path: '/image-recognition',
    icon: markRaw(Camera),
    title: '图像识别',
    desc: '上传图片进行AI识别',
    ariaLabel: '进入图像识别'
  }
]

export default {
  name: 'MenuView',
  setup() {
    const router = useRouter()

    const handleLogout = async () => {
      try {
        await ElMessageBox.confirm('确定要退出登录吗？', '提示', {
          confirmButtonText: '确定',
          cancelButtonText: '取消',
          type: 'warning'
        })
        await logout()
        ElMessage.success('退出登录成功')
        router.replace('/login')
      } catch {
        // 用户取消操作
      }
    }

    return {
      tiles,
      handleLogout
    }
  }
}
</script>

<style scoped>
.menu-container {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  color: var(--mac-text);
  background: var(--mac-bg);
}

/* ---- Window chrome ------------------------------------------------------ */

.toolbar {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex: 0 0 60px;
  padding: 0 clamp(16px, 4vw, 40px);
  border-bottom: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.brand-mark {
  display: grid;
  place-items: center;
  flex: 0 0 auto;
  width: 26px;
  height: 26px;
  border-radius: var(--mac-radius-sm);
  color: var(--mac-white);
  background: var(--mac-accent-bg);
  box-shadow: var(--mac-accent-ring);
  font-size: var(--mac-text-base);
  font-weight: 650;
  user-select: none;
}

.toolbar h1 {
  overflow: hidden;
  font-size: var(--mac-text-lg);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ghost-btn {
  flex: 0 0 auto;
  min-height: 30px;
  padding: 0 12px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-control-bg);
  box-shadow: 0 1px 1.5px rgba(0, 0, 0, 0.035);
  font-size: var(--mac-text-base);
  font-weight: 590;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease);
}

.ghost-btn:hover {
  border-color: var(--mac-border-strong);
  background: var(--mac-control-bg-hover);
}

.ghost-btn:active {
  background: var(--mac-control-bg-active);
}

/* ---- Content ------------------------------------------------------------ */

.main {
  flex: 1;
  width: 100%;
  max-width: 1080px;
  margin: 0 auto;
  padding: clamp(32px, 6vw, 64px) clamp(16px, 4vw, 28px) 64px;
}

.page-intro {
  margin-bottom: clamp(20px, 3vw, 32px);
}

.page-intro h2 {
  font-size: var(--mac-text-3xl);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tighter);
}

.page-intro p {
  margin-top: 6px;
  max-width: 52ch;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-md);
}

.menu-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 14px;
}

.menu-item {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) 12px;
  align-items: center;
  gap: 16px;
  padding: 18px 18px 18px 16px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-lg);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
  text-align: left;
  transition: transform var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.menu-item:hover {
  border-color: var(--mac-border);
  transform: translateY(-1px);
  box-shadow: var(--mac-elev-2);
}

.menu-item:active {
  transform: translateY(0);
  box-shadow: var(--mac-elev-1);
}

.tile-glyph {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text);
  background: var(--mac-surface-strong);
  transition: background var(--mac-dur-fast) var(--mac-ease);
}

.menu-item:hover .tile-glyph {
  color: var(--mac-white);
  background: var(--mac-black);
}

/*
 * The icon components render a bare <svg> sized by its viewBox, so the box is
 * set here rather than relying on the el-icon wrapper.
 */
.tile-glyph :deep(svg) {
  width: 21px;
  height: 21px;
}

.tile-body {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.tile-title {
  font-size: var(--mac-text-lg);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.tile-desc {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  line-height: 1.45;
}

.tile-chevron {
  color: var(--mac-text-quaternary);
  font-size: 20px;
  line-height: 1;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    transform var(--mac-dur-fast) var(--mac-ease);
}

.menu-item:hover .tile-chevron {
  color: var(--mac-text-secondary);
  transform: translateX(2px);
}

@media (max-width: 720px) {
  .menu-grid {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }

  .page-intro h2 {
    font-size: var(--mac-text-2xl);
  }

  .menu-item {
    padding: 15px;
  }
}
</style>
