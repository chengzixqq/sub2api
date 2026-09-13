import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import GroupSelect from '../GroupSelect.vue'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

let unmount: (() => void) | undefined
afterEach(() => {
  unmount?.()
  document.body.innerHTML = ''
})

describe('GroupSelect platform filtering', () => {
  it('intersects authorized platforms with search without changing selection and resets on reopen', async () => {
    const wrapper = mount(GroupSelect, {
      props: {
        modelValue: 1,
        options: [
          { value: 1, label: 'Alpha Claude', platform: 'anthropic' },
          { value: 2, label: 'Alpha OpenAI', platform: 'openai' },
          { value: 3, label: 'Beta OpenAI', platform: 'openai' },
          { value: 4, label: 'Kimi', platform: 'kimi' },
        ],
      },
    })
    unmount = () => wrapper.unmount()
    await wrapper.get('button').trigger('click')
    await nextTick()
    const portal = () => document.body.querySelector<HTMLElement>('.select-dropdown-portal')!
    const labels = () => [...portal().querySelectorAll('.select-option-label')].map(el => el.textContent)
    expect(labels()).toHaveLength(4)
    expect(portal().querySelector('[data-platform="gemini"]')).toBeNull()
    expect(portal().querySelector('[data-platform="kimi"]')).not.toBeNull()
    portal().querySelector<HTMLButtonElement>('[data-platform="openai"]')!.click()
    await nextTick()
    expect(labels()).toEqual(['Alpha OpenAI', 'Beta OpenAI'])
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('button').text()).toContain('Alpha Claude')
    const search = portal().querySelector<HTMLInputElement>('input')!
    search.value = 'Alpha'
    search.dispatchEvent(new Event('input'))
    await nextTick()
    expect(labels()).toEqual(['Alpha OpenAI'])
    expect(portal().querySelector('[data-platform="kimi"]')).not.toBeNull()
    await wrapper.get('button').trigger('click')
    await wrapper.get('button').trigger('click')
    await nextTick()
    expect(labels()).toHaveLength(4)
    expect(portal().querySelector('[data-platform=""]')?.getAttribute('aria-pressed')).toBe('true')
    portal().querySelectorAll<HTMLElement>('[role="option"]')[1].click()
    await nextTick()
    expect(wrapper.emitted('update:modelValue')).toEqual([[2]])
  })
})
