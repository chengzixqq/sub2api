import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const testDir = dirname(fileURLToPath(import.meta.url))
const routerSource = readFileSync(resolve(testDir, '../index.ts'), 'utf8')
const sidebarSource = readFileSync(
  resolve(testDir, '../../components/layout/AppSidebar.vue'),
  'utf8'
)

function routeBlock(path: string, nextPath: string): string {
  const start = routerSource.indexOf(`path: '${path}'`)
  const end = routerSource.indexOf(`path: '${nextPath}'`, start + 1)
  if (start < 0 || end < 0) throw new Error(`route block not found: ${path}`)
  return routerSource.slice(start, end)
}

describe('admin order management access', () => {
  it('keeps the adjustment page owner-only but independent from the payment feature switch', () => {
    const orders = routeBlock('/admin/orders', '/admin/orders/plans')

    expect(orders).toContain('requiresAdmin: true')
    expect(orders).toContain('requiresOwner: true')
    expect(orders).not.toContain('requiresPayment: true')
  })

  it('keeps payment-only dashboard and plan routes behind both owner and payment guards', () => {
    const dashboard = routeBlock('/admin/orders/dashboard', '/admin/orders')
    const plans = routeBlock('/admin/orders/plans', '/:pathMatch(.*)*')

    for (const block of [dashboard, plans]) {
      expect(block).toContain('requiresOwner: true')
      expect(block).toContain('requiresPayment: true')
    }
  })

  it('keeps the sidebar group visible while gating only its payment-specific children', () => {
    const start = sidebarSource.indexOf("path: '/admin/orders'")
    const end = sidebarSource.indexOf("path: '/admin/usage'", start)
    const group = sidebarSource.slice(start, end)
    const parent = group.slice(0, group.indexOf('children:'))

    expect(parent).not.toContain('featureFlag: flagAdminPayment')
    expect(group).toMatch(/\/admin\/orders\/dashboard[^\n]+featureFlag: flagAdminPayment/)
    expect(group).toMatch(/\/admin\/orders\/plans[^\n]+featureFlag: flagAdminPayment/)
    expect(group).toContain("{ path: '/admin/orders', label: t('nav.orderManagement'), icon: OrderIcon }")
  })
})
