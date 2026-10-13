import { describe, expect, it } from 'vitest'
import { safePaymentUrl } from '../paymentUrl'

describe('payment URL validation', () => {
  it.each([
    'https://pay.example.com/GatewayV2/api/pay/submit?sign=a%2Bb%2Fc%3D&name=A+B',
    'http://pay.example.com/checkout',
    'alipay://platformapi/startapp?appId=20000067',
    'alipays://platformapi/startapp?saId=10000007&qrcode=https%3A%2F%2Fpay.example.com',
    'weixin://wxpay/bizpayurl?pr=abc',
  ])('preserves an allowed checkout URL byte for byte: %s', url => {
    expect(safePaymentUrl(url)).toBe(url)
  })

  it.each([
    'javascript:alert(1)', 'JaVaScRiPt://pay.example.com', 'data:text/html,pay',
    'file:///payment', 'intent://payment', '//evil.example.com/pay',
    'https://user:pass@pay.example.com', 'alipays://user@platformapi/startapp',
    'alipays:platformapi/startapp', 'weixin://', 'https://',
    'java\nscript:alert(1)', 'https://pay.example.com/\ncheckout',
    'https://pay.example.com\\@evil.example.com', '/payment/stripe/../../admin',
  ])('rejects an unsafe checkout URL: %s', url => {
    expect(safePaymentUrl(url, true)).toBe('')
  })

  it('allows only known internal payment routes when explicitly requested', () => {
    expect(safePaymentUrl('/payment/stripe?order_id=1')).toBe('')
    expect(safePaymentUrl('/payment/stripe?order_id=1', true)).toBe('/payment/stripe?order_id=1')
    expect(safePaymentUrl('/payment/airwallex?order_id=1', true)).toBe('/payment/airwallex?order_id=1')
    expect(safePaymentUrl('/admin', true)).toBe('')
  })
})
