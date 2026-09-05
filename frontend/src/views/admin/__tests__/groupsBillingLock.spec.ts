import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const groupsViewSource = readFileSync(
  resolve(process.cwd(), 'src/views/admin/GroupsView.vue'),
  'utf8',
)

describe('GroupsView shared workspace billing lock', () => {
  it('keeps create pricing available but hides edit pricing for a locked group', () => {
    expect(groupsViewSource.match(/v-if="canBillGroups"/g)).toHaveLength(2)
    expect(groupsViewSource).toContain(
      'v-if="canBillGroups && !editingGroup?.billing_locked"',
    )
  })
})
