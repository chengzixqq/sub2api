import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import Select from '@/components/common/Select.vue'
import PaymentProviderDialog from '@/components/payment/PaymentProviderDialog.vue'
import { STRIPE_SDK_API_VERSION } from '@/components/payment/providerConfig'
import type { ProviderInstance } from '@/types/payment'

const messages: Record<string, string> = {
  'admin.settings.payment.providerConfig': 'Credentials',
  'admin.settings.payment.refundEnabled': 'Refund enabled',
  'admin.settings.payment.allowUserRefund': 'User refund enabled',
  'admin.settings.payment.easypayCustomMethods': 'Custom EasyPay methods',
  'admin.settings.payment.easypayCustomMethodsHint': 'Add provider-specific EasyPay type values.',
  'admin.settings.payment.addCustomMethod': 'Add method',
  'admin.settings.payment.customMethodType': 'Payment type',
  'admin.settings.payment.customMethodUpstreamType': 'Upstream type',
  'admin.settings.payment.customMethodDisplayName': 'Display name',
  'admin.settings.payment.customMethodDisplayNamePlaceholder': '信用卡',
  'admin.settings.payment.paymentGuideTrigger': 'View payment guide',
  'admin.settings.payment.alipayGuideSummary': 'Desktop prefers QR precreate and falls back to cashier; mobile prefers WAP checkout.',
  'admin.settings.payment.wxpayGuideSummary': 'Desktop prefers Native QR; mobile routes to JSAPI or H5 based on browser context.',
  'admin.settings.payment.airwallexGuideSummary': 'Use Payment Acceptance read/write only.',
  'admin.settings.payment.stripeWebhookHint': 'Configure Stripe webhook.',
  'admin.settings.payment.stripeWebhookApiVersionHint': 'Use Stripe API version {version}.',
  'admin.settings.payment.airwallexWebhookHint': 'Select payment_intent.succeeded and use the latest stable API version.',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string>) => {
      const message = messages[key] ?? key
      if (!params) return message
      return Object.entries(params).reduce(
        (value, [name, replacement]) => value.replaceAll(`{${name}}`, replacement),
        message,
      )
    },
  }),
}))

function providerFactory(overrides: Partial<ProviderInstance> = {}): ProviderInstance {
  return {
    id: 1,
    provider_key: 'airwallex',
    name: 'Airwallex',
    config: {},
    supported_types: ['airwallex'],
    enabled: true,
    payment_mode: '',
    refund_enabled: false,
    allow_user_refund: false,
    limits: '',
    sort_order: 0,
    ...overrides,
  }
}

function mountDialog(options: { editing?: ProviderInstance | null } = {}) {
  return mount(PaymentProviderDialog, {
    props: {
      show: true,
      saving: false,
      editing: options.editing ?? null,
      allKeyOptions: [
        { value: 'easypay', label: 'EasyPay' },
        { value: 'jupitong', label: 'JuFuTong' },
        { value: 'alipay', label: 'Alipay' },
        { value: 'wxpay', label: 'WeChat Pay' },
        { value: 'stripe', label: 'Stripe' },
        { value: 'airwallex', label: 'Airwallex' },
      ],
      enabledKeyOptions: [
        { value: 'easypay', label: 'EasyPay' },
        { value: 'jupitong', label: 'JuFuTong' },
        { value: 'alipay', label: 'Alipay' },
        { value: 'wxpay', label: 'WeChat Pay' },
        { value: 'airwallex', label: 'Airwallex' },
      ],
      allPaymentTypes: [
        { value: 'alipay', label: 'Alipay' },
        { value: 'wxpay', label: 'WeChat Pay' },
      ],
      redirectLabel: 'Redirect',
    },
    global: {
      stubs: {
        BaseDialog: {
          template: '<div><slot /><slot name="footer" /></div>',
        },
        Select: {
          props: ['modelValue', 'options', 'disabled'],
          template: '<div />',
        },
        ToggleSwitch: {
          props: ['label'],
          template: '<span>{{ label }}</span>',
        },
      },
    },
  })
}

describe('PaymentProviderDialog callback URLs', () => {
  it('defaults legacy EasyPay to V1 with an explicit refund code and preserves masked secrets', async () => {
    const provider = providerFactory({ provider_key: 'easypay', config: { pid: '123', pkey: '********', apiBase: 'https://pay.example.com/GatewayV2' } })
    const wrapper = mountDialog({ editing: provider })
    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()
    expect((wrapper.get('[data-config-field="pkey"] input').element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[data-config-field="privateKey"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
    expect(payload.config).toMatchObject({ apiVersion: 'v1', v1RefundSuccessCode: '1', apiBase: 'https://pay.example.com/GatewayV2' })
    expect(payload.config.pkey).toBeUndefined()
    wrapper.get('[data-config-field="v1RefundSuccessCode"]').findComponent(Select).vm.$emit('update:modelValue', '0')
    await nextTick()
    await wrapper.get('form').trigger('submit')
    expect((wrapper.emitted('save')?.[1]?.[0] as { config: Record<string, string> }).config.v1RefundSuccessCode).toBe('0')
    wrapper.unmount()
  })

  it('requires V2 RSA keys and drops V1 credentials and CIDs when switching a new instance', async () => {
    const wrapper = mountDialog()
    ;(wrapper.vm as unknown as { reset: (key: string) => void }).reset('easypay')
    await nextTick()
    await wrapper.get('input[type="text"]').setValue('V2 gateway')
    await wrapper.get('[data-config-field="pid"] input').setValue('123')
    await wrapper.get('[data-config-field="apiBase"] input').setValue('https://pay.example.com/GatewayV2')
    await wrapper.get('[data-config-field="pkey"] input').setValue('v1-secret')
    await wrapper.get('[data-config-field="cidAlipay"] input').setValue('77')
    wrapper.get('[data-config-field="apiVersion"]').findComponent(Select).vm.$emit('update:modelValue', 'v2')
    await nextTick()
    expect(wrapper.find('[data-config-field="pkey"]').exists()).toBe(false)
    expect(wrapper.find('[data-config-field="cidAlipay"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    await wrapper.get('[data-config-field="privateKey"] textarea').setValue('merchant-private-key')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    await wrapper.get('[data-config-field="publicKey"] textarea').setValue('platform-public-key')
    await wrapper.get('form').trigger('submit')
    const payload = wrapper.emitted('save')?.[0]?.[0] as { provider_key: string; config: Record<string, string> }
    expect(payload.provider_key).toBe('easypay')
    expect(payload.config).toMatchObject({ apiVersion: 'v2', privateKey: 'merchant-private-key', publicKey: 'platform-public-key', apiBase: 'https://pay.example.com/GatewayV2' })
    expect(payload.config.pkey).toBeUndefined()
    expect(payload.config.cidAlipay).toBeUndefined()
    expect(payload.config.v1RefundSuccessCode).toBeUndefined()
    wrapper.unmount()
  })

  it('retains existing V2 secrets when editing unrelated provider fields', async () => {
    const provider = providerFactory({ provider_key: 'easypay', config: { apiVersion: 'v2', pid: '123', privateKey: '********', publicKey: '********', apiBase: 'https://pay.example.com/GatewayV2' } })
    const wrapper = mountDialog({ editing: provider })
    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()
    await wrapper.get('input[type="text"]').setValue('Renamed gateway')
    await wrapper.get('form').trigger('submit')
    const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
    expect(payload.config.apiVersion).toBe('v2')
    expect(payload.config.privateKey).toBeUndefined()
    expect(payload.config.publicKey).toBeUndefined()
    wrapper.unmount()
  })

  it('requires a fresh V1 secret when switching an unused V2 instance and restores the default refund code', async () => {
    const provider = providerFactory({ provider_key: 'easypay', config: { apiVersion: 'v2', pid: '123', apiBase: 'https://pay.example.com' } })
    const wrapper = mountDialog({ editing: provider })
    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()
    wrapper.get('[data-config-field="apiVersion"]').findComponent(Select).vm.$emit('update:modelValue', 'v1')
    await nextTick()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    await wrapper.get('[data-config-field="pkey"] input').setValue('new-v1-secret')
    await wrapper.get('form').trigger('submit')
    expect((wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }).config).toMatchObject({ apiVersion: 'v1', pkey: 'new-v1-secret', v1RefundSuccessCode: '1' })
    wrapper.unmount()
  })

  it('requires PID and PKey when creating a JuFuTong provider', async () => {
    const wrapper = mountDialog()
    ;(wrapper.vm as unknown as { reset: (key: string) => void }).reset('jupitong')
    await nextTick()

    await wrapper.find('input[type="text"]').setValue('JuFuTong')
    await wrapper.find('form').trigger('submit.prevent')
    expect(wrapper.emitted('save')).toBeUndefined()

    const pidInput = wrapper.findAll('input[type="text"]').find(input => input.classes().includes('input') && input.element !== wrapper.find('input[type="text"]').element)
    if (!pidInput) throw new Error('PID input not found')
    await pidInput.setValue('merchant-1')
    await wrapper.find('form').trigger('submit.prevent')
    expect(wrapper.emitted('save')).toBeUndefined()

    await wrapper.find('input[type="password"]').setValue('secret-1')
    await wrapper.find('form').trigger('submit.prevent')
    const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
    expect(payload.config.pid).toBe('merchant-1')
    expect(payload.config.pkey).toBe('secret-1')
  })

  it('uses the dedicated JuFuTong callback and keeps refunds disabled', async () => {
    const provider = providerFactory({
      provider_key: 'jupitong',
      name: 'JuFuTong',
      config: { pid: 'merchant-1', apiBase: 'https://untrusted.example.com', extra: 'legacy' },
      supported_types: ['alipay', 'wxpay'],
      refund_enabled: true,
      allow_user_refund: true,
    })
    const wrapper = mountDialog({ editing: provider })
    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()

    expect(wrapper.text()).not.toContain('apiBase')
    expect(wrapper.text()).not.toContain('Refund enabled')
    expect(wrapper.text()).not.toContain('User refund enabled')
    expect(wrapper.findAll('input[type="text"]').some(input => (input.element as HTMLInputElement).value === 'https://untrusted.example.com')).toBe(false)
    await wrapper.find('form').trigger('submit.prevent')

    const payload = wrapper.emitted('save')?.[0]?.[0] as {
      config: Record<string, string>
      refund_enabled: boolean
      allow_user_refund: boolean
    }
    expect(payload.config.notifyUrl).toBe(window.location.origin + '/api/v1/payment/webhook/jupitong')
    expect(payload.config.returnUrl).toBe(window.location.origin + '/payment/result')
    expect(payload.config.apiBase).toBeUndefined()
    expect(payload.config.extra).toBeUndefined()
    expect(payload.refund_enabled).toBe(false)
    expect(payload.allow_user_refund).toBe(false)
  })

  it.each([
    ['https://notify.example.com/', 'https://return.example.com///', 'https://notify.example.com', 'https://return.example.com'],
    [' https://notify.example.com/sub/ ', ' https://return.example.com/site/ ', 'https://notify.example.com/sub', 'https://return.example.com/site'],
    ['https://notify.example.com', 'https://return.example.com', 'https://notify.example.com', 'https://return.example.com'],
    ['', '', window.location.origin, window.location.origin],
  ])('joins callback paths to %s and %s', async (notify, returnUrl, expectedNotify, expectedReturn) => {
    const provider = providerFactory({
      provider_key: 'easypay', name: 'EasyPay',
      config: { pid: 'pid-1', apiBase: 'https://pay.example.com' },
      supported_types: ['alipay'], payment_mode: 'qrcode',
    })
    const wrapper = mountDialog({ editing: provider })
    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()
    const bases = wrapper.findAll('input').filter(input => input.classes().includes('!rounded-r-none'))
    await bases[0].setValue(notify)
    await bases[1].setValue(returnUrl)
    await wrapper.find('form').trigger('submit')
    const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
    expect(payload.config.notifyUrl).toBe(expectedNotify + '/api/v1/payment/webhook/easypay')
    expect(payload.config.returnUrl).toBe(expectedReturn + '/payment/result')
    wrapper.unmount()
  })
})

describe('PaymentProviderDialog payment guide', () => {
  it('shows no payment guide for providers without a flow guide', () => {
    const wrapper = mountDialog()

    expect(wrapper.text()).not.toContain(messages['admin.settings.payment.alipayGuideSummary'])
    expect(wrapper.text()).not.toContain(messages['admin.settings.payment.wxpayGuideSummary'])
    expect(wrapper.find('button[title="View payment guide"]').exists()).toBe(false)
  })

  it.each([
    ['alipay', 'admin.settings.payment.alipayGuideSummary'],
    ['wxpay', 'admin.settings.payment.wxpayGuideSummary'],
    ['airwallex', 'admin.settings.payment.airwallexGuideSummary'],
  ])('shows the payment guide summary for %s', async (providerKey, summaryKey) => {
    const wrapper = mountDialog()

    ;(wrapper.vm as unknown as { reset: (key: string) => void }).reset(providerKey)
    await nextTick()

    expect(wrapper.text()).toContain(messages[summaryKey])
    expect(wrapper.find('button[title="View payment guide"]').exists()).toBe(true)
  })

  it('shows Airwallex webhook event and API version guidance with the webhook URL', async () => {
    const wrapper = mountDialog()

    ;(wrapper.vm as unknown as { reset: (key: string) => void }).reset('airwallex')
    await nextTick()

    expect(wrapper.text()).toContain(messages['admin.settings.payment.airwallexWebhookHint'])
    expect(wrapper.text()).toContain('/api/v1/payment/webhook/airwallex')
  })

  it('shows Stripe webhook API version guidance with the integrated SDK version', async () => {
    const wrapper = mountDialog()

    ;(wrapper.vm as unknown as { reset: (key: string) => void }).reset('stripe')
    await nextTick()

    expect(wrapper.text()).toContain(messages['admin.settings.payment.stripeWebhookHint'])
    expect(wrapper.text()).toContain(`Use Stripe API version ${STRIPE_SDK_API_VERSION}.`)
    expect(wrapper.text()).toContain('/api/v1/payment/webhook/stripe')
  })

  it('emits an empty Airwallex accountId when the admin clears it', async () => {
    const provider = providerFactory({
      config: {
        clientId: 'cid_123',
        apiBase: 'https://api.airwallex.com/api/v1',
        countryCode: 'CN',
        currency: 'CNY',
        accountId: 'acct_123',
      },
    })
    const wrapper = mountDialog({ editing: provider })

    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()

    const accountIdInput = wrapper
      .findAll('input[type="text"]')
      .find(input => (input.element as HTMLInputElement).value === 'acct_123')
    if (!accountIdInput) throw new Error('accountId input not found')

    await accountIdInput.setValue('')
    await wrapper.find('form').trigger('submit.prevent')

    const payload = wrapper.emitted('save')?.[0]?.[0] as { config: Record<string, string> }
    expect(payload.config.accountId).toBe('')
  })

  it.each(['epay', 'usdt.trc20'])('serializes EasyPay upstream type %s and adds the local type to supported_types', async (upstreamType) => {
    const provider = providerFactory({
      provider_key: 'easypay',
      name: 'EasyPay',
      config: {
        pid: 'pid-1',
        apiBase: 'https://pay.example.com',
        notifyUrl: 'https://example.com/api/v1/payment/webhook/easypay',
        returnUrl: 'https://example.com/payment/result',
      },
      supported_types: ['alipay', 'wxpay'],
      payment_mode: 'qrcode',
    })
    const wrapper = mountDialog({ editing: provider })

    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()

    await wrapper.find('button.btn-sm').trigger('click')
    await nextTick()

    const inputs = wrapper.findAll('input[type="text"]')
    const customTypeInputs = inputs.filter(input => (input.element as HTMLInputElement).placeholder === 'credit_card')
    const ldcTypeInput = customTypeInputs[0]
    const upstreamTypeInput = customTypeInputs[1]
    const displayNameInput = inputs.find(input => (input.element as HTMLInputElement).placeholder === '信用卡')
    if (!ldcTypeInput || !upstreamTypeInput || !displayNameInput) {
      throw new Error('custom method inputs not found')
    }

    await ldcTypeInput.setValue('ldc')
    await upstreamTypeInput.setValue(upstreamType)
    await displayNameInput.setValue('LDC')
    await wrapper.find('form').trigger('submit.prevent')

    const payload = wrapper.emitted('save')?.[0]?.[0] as {
      config: Record<string, string>
      supported_types: string[]
    }
    expect(JSON.parse(payload.config.customMethods)).toEqual([{ type: 'ldc', upstreamType, displayName: 'LDC' }])
    expect(payload.supported_types).toEqual(['alipay', 'wxpay', 'ldc'])
  })

  it.each([
    ['alipay_hk', 'hkpay'],
    ['usdt.trc20', 'usdt.trc20'],
    ['usdt_trc20', 'usdt/trc20'],
  ])('rejects invalid EasyPay mapping %s to %s', async (type, upstreamType) => {
    const provider = providerFactory({
      provider_key: 'easypay',
      name: 'EasyPay',
      config: {
        pid: 'pid-1',
        apiBase: 'https://pay.example.com',
        notifyUrl: 'https://example.com/api/v1/payment/webhook/easypay',
        returnUrl: 'https://example.com/payment/result',
      },
      supported_types: ['alipay', 'wxpay'],
      payment_mode: 'qrcode',
    })
    const wrapper = mountDialog({ editing: provider })

    ;(wrapper.vm as unknown as { loadProvider: (provider: ProviderInstance) => void }).loadProvider(provider)
    await nextTick()

    await wrapper.find('button.btn-sm').trigger('click')
    await nextTick()

    const inputs = wrapper.findAll('input[type="text"]')
    const customTypeInputs = inputs.filter(input => (input.element as HTMLInputElement).placeholder === 'credit_card')
    const typeInput = customTypeInputs[0]
    const upstreamTypeInput = customTypeInputs[1]
    const displayNameInput = inputs.find(input => (input.element as HTMLInputElement).placeholder === '信用卡')
    if (!typeInput || !upstreamTypeInput || !displayNameInput) {
      throw new Error('custom method inputs not found')
    }

    await typeInput.setValue(type)
    await upstreamTypeInput.setValue(upstreamType)
    await displayNameInput.setValue('Custom payment')
    await wrapper.find('form').trigger('submit.prevent')

    expect(wrapper.emitted('save')).toBeUndefined()
  })
})
