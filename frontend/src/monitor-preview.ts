import { createApp } from 'vue'
import { createPinia } from 'pinia'
import i18n, { initI18n } from './i18n'
import MonitorPreviewView from './views/dev/MonitorPreviewView.vue'
import './style.css'

function applyTheme() {
  const saved = localStorage.getItem('theme')
  const dark = saved === 'dark' || (!saved && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
}

async function mountPreview() {
  applyTheme()
  await initI18n()
  createApp(MonitorPreviewView).use(createPinia()).use(i18n).mount('#monitor-preview')
}

void mountPreview()
