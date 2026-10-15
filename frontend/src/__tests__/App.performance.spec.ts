import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import App from '@/App.vue'
import '@/router'
import { apiClient } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useSubscriptionStore } from '@/stores/subscriptions'
import { useAdminComplianceStore } from '@/stores/adminCompliance'
import { useAppStore } from '@/stores/app'

const h = vi.hoisted(() => ({
  guard: null as any,
  route: { path:'/admin/dashboard',fullPath:'/admin/dashboard',name:'AdminDashboard',params:{},meta:{ requiresAuth:true, requiresAdmin:true, requiresOwner:true,title:'Admin Dashboard' } },
  events: [] as any[],
  base: 0, needsSetup:false, setupFailure:false, publicDelay:100, replace:vi.fn(),
}))
vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  createWebHistory: vi.fn(),
  createRouter: () => ({ beforeEach:(f:any)=>{h.guard=f},afterEach:vi.fn(),onError:vi.fn() }),
  useRouter: () => ({afterEach:vi.fn(),replace:h.replace}),
  useRoute: () => h.route,
}))
vi.mock('@/composables/useRoutePrefetch', () => ({useRoutePrefetch:()=>({triggerPrefetch:vi.fn()})}))
vi.mock('@/composables/useNavigationLoading', () => ({useNavigationLoadingState:()=>({startNavigation:vi.fn(),endNavigation:vi.fn()})}))
vi.mock('@/api/setup', () => ({getSetupStatus: () => {
  h.events.push({url:'/setup/status',phase:'start',ms:Date.now()-h.base})
  return new Promise((resolve,reject)=>setTimeout(()=>{
    h.events.push({url:'/setup/status',phase:'end',ms:Date.now()-h.base})
    if(h.setupFailure)reject(new Error('fixture setup failure'))
    else resolve({needs_setup:h.needsSetup,step:'done'})
  },100))
}}))

function installAdapter(overrides:(config:any)=>any = () => undefined) {
  apiClient.defaults.adapter = async config => {
    const url=String(config.url)
    h.events.push({url,phase:'start',ms:Date.now()-h.base,params:config.params})
    await new Promise(resolve=>setTimeout(resolve,url==='/settings/public'?h.publicDelay:100))
    let data:any=overrides(config)
    if(data===undefined) {
      if(url==='/auth/me') data={id:1,email:'fixture@example.invalid',role:'admin',run_mode:'standard'}
      else if(url==='/admin/compliance') data={required:false,version:String(config.headers.Authorization)}
      else if(url==='/settings/public') data={site_name:'Fixture',subscription_enabled:false,payment_enabled:false}
      else data=[]
    }
    h.events.push({url,phase:'end',ms:Date.now()-h.base})
    return {status:200,statusText:'OK',headers:{},config,data:{code:0,data}}
  }
}


const mountApp = () => mount(App,{global:{stubs:{RouterView:true,Toast:true,NavigationProgress:true,AnnouncementPopup:true,AdminComplianceDialog:true}}})
const complianceStarts=()=>h.events.filter(e=>e.url==='/admin/compliance'&&e.phase==='start')

describe('App first-load requests',()=>{
 beforeEach(()=>{
  vi.useFakeTimers();vi.setSystemTime(new Date('2026-10-15T12:00:00Z'));h.base=Date.now();h.events=[]
  h.needsSetup=false;h.setupFailure=false;h.publicDelay=100;h.replace.mockReset()
  localStorage.clear();setActivePinia(createPinia())
  const auth=useAuthStore();auth.user={id:1,email:'fixture@example.invalid',role:'admin'} as any;auth.token='fixture-token'
  localStorage.setItem('auth_token','fixture-token');localStorage.setItem('auth_user',JSON.stringify(auth.user))
  installAdapter()
 })
 afterEach(()=>{useSubscriptionStore().clear();vi.clearAllTimers();vi.useRealTimers()})
 it('reuses the first navigation compliance result and starts settings beside setup',async()=>{
  const next=vi.fn();const navigation=h.guard(h.route,{},next)
  await vi.advanceTimersByTimeAsync(100);await navigation
  const wrapper=mountApp();await flushPromises()
  await vi.advanceTimersByTimeAsync(200);await flushPromises()
  expect(next).toHaveBeenCalledOnce()
  expect(complianceStarts()).toHaveLength(1)
  expect(h.events.find(e=>e.url==='/settings/public'&&e.phase==='start').ms).toBe(100)
  wrapper.unmount()
 })
 it('shares a pending App compliance request with a concurrent navigation',async()=>{
  const wrapper=mountApp();await flushPromises()
  const navigation=h.guard(h.route,{},vi.fn())
  await vi.advanceTimersByTimeAsync(100);await navigation
  expect(complianceStarts()).toHaveLength(1)
  wrapper.unmount()
 })
 it('clears the old identity and ignores its response while the new identity is pending',async()=>{
  const wrapper=mountApp();await flushPromises()
  await vi.advanceTimersByTimeAsync(25)
  const auth=useAuthStore();auth.user={id:2,email:'other@example.invalid',role:'admin'} as any;auth.token='other-fixture-token'
  localStorage.setItem('auth_token','other-fixture-token')
  await flushPromises()
  expect(complianceStarts()).toHaveLength(2)
  await vi.advanceTimersByTimeAsync(75)
  const compliance=useAdminComplianceStore()
  expect(compliance.status).toBeNull();expect(compliance.initialized).toBe(false);expect(compliance.loading).toBe(true)
  await vi.advanceTimersByTimeAsync(25)
  expect(compliance.status?.version).toBe('Bearer other-fixture-token')
  wrapper.unmount()
 })
 it('resets compliance when the same user loses its admin role',async()=>{
  const wrapper=mountApp();await flushPromises();await vi.advanceTimersByTimeAsync(100)
  expect(useAdminComplianceStore().initialized).toBe(true)
  useAuthStore().user={id:1,role:'user'} as any
  await flushPromises()
  expect(useAdminComplianceStore().status).toBeNull()
  expect(useAdminComplianceStore().initialized).toBe(false)
  expect(complianceStarts()).toHaveLength(1)
  wrapper.unmount()
 })
 it('redirects an unfinished setup without waiting for slower public settings',async()=>{
  h.needsSetup=true;h.publicDelay=500
  const wrapper=mountApp();await flushPromises();await vi.advanceTimersByTimeAsync(100)
  expect(h.replace).toHaveBeenCalledWith('/setup')
  expect(h.events.filter(e=>e.url==='/settings/public'&&e.phase==='end')).toHaveLength(0)
  wrapper.unmount();await vi.advanceTimersByTimeAsync(400)
 })
 it('keeps the public-settings fallback when setup status fails',async()=>{
  h.setupFailure=true
  const wrapper=mountApp();await flushPromises();await vi.advanceTimersByTimeAsync(100)
  expect(h.replace).not.toHaveBeenCalled()
  expect(useAppStore().publicSettingsLoaded).toBe(true)
  wrapper.unmount()
 })
})
