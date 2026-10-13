/** Validate without serializing again: signed checkout query strings must remain intact. */
export function safePaymentUrl(raw: string | undefined, allowInternal = false): string {
  if (!raw || raw.includes('\\')) return ''
  for (const character of raw) {
    const code = character.charCodeAt(0)
    if (code < 32 || code === 127) return ''
  }
  const value = raw.trim()
  if (allowInternal && /^\/payment\/(?:stripe|airwallex)(?:[?#]|$)/.test(value)) return value
  if (!/^(?:https?|alipay|alipays|weixin):\/\//i.test(value)) return ''
  try {
    const url = new URL(value)
    if (!url.hostname || url.username || url.password) return ''
    return value
  } catch {
    return ''
  }
}
