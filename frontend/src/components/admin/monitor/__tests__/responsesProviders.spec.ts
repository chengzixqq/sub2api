import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import type {
  AssociatedMonitorBrief,
  ChannelMonitorTemplate,
} from '@/api/admin/channelMonitorTemplate'
import MonitorAdvancedRequestConfig from '@/components/admin/monitor/MonitorAdvancedRequestConfig.vue'
import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import MonitorTemplateApplyPickerDialog from '@/components/admin/monitor/MonitorTemplateApplyPickerDialog.vue'
import MonitorTemplateManagerDialog from '@/components/admin/monitor/MonitorTemplateManagerDialog.vue'

const {
  monitorCreate,
  monitorUpdate,
  templateList,
  templateCreate,
  templateUpdate,
  templateDelete,
  templateListAssociated,
  templateApply,
  accountsList,
  accountsGetById,
  keysList,
  userGroupRates,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  monitorCreate: vi.fn(),
  monitorUpdate: vi.fn(),
  templateList: vi.fn(),
  templateCreate: vi.fn(),
  templateUpdate: vi.fn(),
  templateDelete: vi.fn(),
  templateListAssociated: vi.fn(),
  templateApply: vi.fn(),
  accountsList: vi.fn(),
  accountsGetById: vi.fn(),
  keysList: vi.fn(),
  userGroupRates: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitor: {
      create: monitorCreate,
      update: monitorUpdate,
    },
    channelMonitorTemplate: {
      list: templateList,
      create: templateCreate,
      update: templateUpdate,
      del: templateDelete,
      listAssociatedMonitors: templateListAssociated,
      apply: templateApply,
    },
    accounts: {
      list: accountsList,
      getById: accountsGetById,
    },
  },
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: keysList },
}))

vi.mock('@/api/groups', () => ({
  userGroupsAPI: { getUserGroupRates: userGroupRates },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    showError,
    showSuccess,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const SelectStub = defineComponent({
  props: {
    options: { type: Array, default: () => [] },
  },
  template: `
    <div class="select-stub">
      <span v-for="option in options" :key="option.value" class="select-option">
        {{ option.label }}
      </span>
    </div>
  `,
})

const EmptyStub = defineComponent({
  template: '<div />',
})

function makeTemplate(overrides: Partial<ChannelMonitorTemplate> = {}): ChannelMonitorTemplate {
  return {
    id: 1,
    name: 'OpenCode template',
    provider: 'opencode_go',
    api_mode: 'responses',
    description: '',
    extra_headers: {},
    body_override_mode: 'off',
    body_override: null,
    created_at: '2026-09-16T00:00:00Z',
    updated_at: '2026-09-16T00:00:00Z',
    associated_monitors: 0,
    ...overrides,
  }
}

function commonStubs() {
  return {
    BaseDialog: BaseDialogStub,
    Toggle: EmptyStub,
    Select: SelectStub,
    ModelTagInput: EmptyStub,
    MonitorKeyPickerDialog: EmptyStub,
    MonitorAdvancedRequestConfig: EmptyStub,
    ProviderIcon: EmptyStub,
    Icon: EmptyStub,
    ConfirmDialog: EmptyStub,
    MonitorTemplateApplyPickerDialog: EmptyStub,
  }
}

function findButton(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button, `button containing "${text}" not found`).toBeDefined()
  return button!
}

const mountedWrappers: VueWrapper[] = []

function mountForm(monitor: ChannelMonitor | null = null) {
  const wrapper = mount(MonitorFormDialog, {
    props: { show: true, monitor },
    global: { stubs: commonStubs() },
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

function mountManager() {
  const wrapper = mount(MonitorTemplateManagerDialog, {
    props: { show: true },
    global: { stubs: commonStubs() },
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

beforeEach(() => {
  monitorCreate.mockResolvedValue({})
  monitorUpdate.mockResolvedValue({})
  templateList.mockResolvedValue({ items: [] })
  templateCreate.mockResolvedValue(makeTemplate())
  templateUpdate.mockResolvedValue(makeTemplate())
  templateDelete.mockResolvedValue(undefined)
  templateListAssociated.mockResolvedValue({ items: [] })
  templateApply.mockResolvedValue({ affected: 0 })
  accountsList.mockResolvedValue({ items: [] })
  accountsGetById.mockReset()
  keysList.mockResolvedValue({ items: [] })
  userGroupRates.mockResolvedValue({})
})

describe('OpenCode Responses support in channel monitor configuration', () => {
  it('shows the API mode picker and preserves Responses in an OpenCode monitor payload', async () => {
    const wrapper = mountForm()
    await flushPromises()

    await wrapper.get('[data-testid="monitor-provider-opencode_go"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.channelMonitor.form.apiModeResponses')
    const responseButton = findButton(wrapper, 'admin.channelMonitor.form.apiModeResponses')
    await responseButton.trigger('click')
    await wrapper.get('input[type="text"]').setValue('opencode probe')
    await wrapper.get('[data-testid="monitor-primary-model"]').setValue('gpt-5.6-luna')
    await wrapper.get('#channel-monitor-form').trigger('submit')
    await flushPromises()

    expect(monitorCreate).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'opencode_go',
      api_mode: 'responses',
    }))
  })

  it('filters OpenCode templates by API mode and keeps the selected mode across provider switches', async () => {
    templateList.mockResolvedValue({
      items: [
        makeTemplate({ id: 10, name: 'OpenCode chat', api_mode: 'chat_completions' }),
        makeTemplate({ id: 11, name: 'OpenCode responses', api_mode: 'responses' }),
      ],
    })
    const wrapper = mountForm()
    await flushPromises()

    await wrapper.get('[data-testid="monitor-provider-opencode_go"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('.select-stub').text()).toContain('OpenCode chat')
    expect(wrapper.find('.select-stub').text()).not.toContain('OpenCode responses')

    await findButton(wrapper, 'admin.channelMonitor.form.apiModeResponses').trigger('click')
    await flushPromises()
    expect(wrapper.find('.select-stub').text()).toContain('OpenCode responses')
    expect(wrapper.find('.select-stub').text()).not.toContain('OpenCode chat')

    await wrapper.get('[data-testid="monitor-provider-openai"]').trigger('click')
    await flushPromises()
    expect(findButton(wrapper, 'admin.channelMonitor.form.apiModeResponses').attributes('aria-pressed')).toBe('true')

    await wrapper.get('[data-testid="monitor-provider-anthropic"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('admin.channelMonitor.form.apiModeResponses')
  })
})

describe('OpenCode Responses templates', () => {
  it('renders a Responses badge and sends api_mode when creating an OpenCode template', async () => {
    templateList.mockResolvedValue({ items: [makeTemplate()] })
    const wrapper = mountManager()
    await flushPromises()

    await findButton(wrapper, 'monitorCommon.providers.opencode_go').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.channelMonitor.form.apiModeResponses')

    await findButton(wrapper, 'admin.channelMonitor.template.createButton').trigger('click')
    await flushPromises()
    await findButton(wrapper, 'admin.channelMonitor.form.apiModeResponses').trigger('click')
    await wrapper.get('input[type="text"]').setValue('new OpenCode template')
    await findButton(wrapper, 'common.create').trigger('click')
    await flushPromises()

    expect(templateCreate).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'opencode_go',
      api_mode: 'responses',
    }))
  })
})

describe('OpenCode advanced request and apply-picker presentation', () => {
  it('uses the Responses body contract and OpenCode chat model in placeholders', async () => {
    const wrapper = mount(MonitorAdvancedRequestConfig, {
      props: {
        provider: 'opencode_go',
        apiMode: 'responses',
        extraHeaders: {},
        bodyOverrideMode: 'replace',
        bodyOverride: null,
      },
    })
    mountedWrappers.push(wrapper)
    const textarea = wrapper.get('textarea')
    expect(textarea.attributes('placeholder')).toContain('"instructions"')
    expect(textarea.attributes('placeholder')).toContain('"input"')
    expect(textarea.attributes('placeholder')).toContain('gpt-5.6-luna')

    await wrapper.setProps({ apiMode: 'chat_completions' })
    expect(wrapper.get('textarea').attributes('placeholder')).toContain('"messages"')
    expect(wrapper.get('textarea').attributes('placeholder')).toContain('glm-5.3')
  })

  it('shows api_mode for OpenCode monitors in the template apply picker', async () => {
    const monitor: AssociatedMonitorBrief = {
      id: 7,
      name: 'OpenCode monitor',
      provider: 'opencode_go',
      api_mode: 'responses',
      enabled: true,
    }
    templateListAssociated.mockResolvedValue({ items: [monitor] })
    const wrapper = mount(MonitorTemplateApplyPickerDialog, {
      props: { show: true, templateId: 1, templateName: 'OpenCode template' },
      global: { stubs: { BaseDialog: BaseDialogStub } },
    })
    mountedWrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.text()).toContain('responses')
  })
})
