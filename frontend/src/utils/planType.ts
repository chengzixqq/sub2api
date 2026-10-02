/**
 * ChatGPT（OpenAI）订阅档位 plan_type 的解析与展示映射。
 *
 * plan_type 由上游原样透传，同一档位会出现 `chatgpt_pro`、`self_serve_business_prolite`
 * 这类下划线/别名写法，因此匹配前一律先归一化。
 *
 * 这里的档位命名只对 OpenAI 平台成立：Antigravity 的 `Pro`、Grok 的 `pro` 是各自产品线的
 * 档位，套用 ChatGPT 的倍率命名（Pro 5x / Pro 20x）会显示错误。
 */

/**
 * plan_type 归一化：去首尾空白、转小写，并去掉空格/下划线/连字符。
 * 例：`self_serve_business_prolite` → `selfservebusinessprolite`。
 */
/** OpenAI subscription SKU identity and Codex display names. Other providers use their own labels. */
export function normalizePlanType(value?: string | null): string {
  return (value || '').trim().toLowerCase().replace(/[\s_-]+/g, '')
}

/**
 * ChatGPT 档位 → 展示标签；未知档位返回空串，由调用方决定是否回退为原始值。
 *
 * Pro 的倍率命名：`pro`/`chatgptpro` 为 Pro 20x，`prolite` 为 Pro 5x；
 * Team/Business：`team` 为 Business Standard，`self_serve_business_prolite` 为 Business Premium。
 */
/** Canonical comparison key; labels may be shared by distinct SKUs. */
export function openAIPlanTypeKey(value?: string | null): string {
  const key = normalizePlanType(value)
  return key === 'chatgptpro' ? 'pro' : key
}

// Wire names from openai/codex b1e72963, protocol/src/account.rs.
export const openAIPlanTypes = [
  'free', 'go', 'plus', 'prolite', 'pro', 'promax', 'team',
  'self_serve_business_usage_based', 'self_serve_business_prolite', 'business',
  'enterprise', 'ent26', 'enterprise_cbp_usage_based', 'enterprise_cbp_automation',
  'edu', 'edu_plus', 'edu_pro', 'unknown'
] as const

/** Keep Status SKU names distinct from the account families used in analytics. */
export function openAIPlanTypeLabel(value?: string | null, display: 'status' | 'analytics' = 'status'): string {
  switch (openAIPlanTypeKey(value)) {
    case 'free': return 'Free'
    case 'go': return 'Go'
    case 'plus': return 'Plus'
    case 'prolite': return 'Pro 5x'
    case 'pro': return 'Pro 20x'
    case 'promax': return 'Pro 500'
    case 'team':
    case 'selfservebusinessusagebased': return 'Business Standard'
    case 'business': return display === 'status' ? 'Enterprise' : 'Business'
    case 'selfservebusinessprolite': return 'Business Premium'
    case 'enterprisecbpautomation': return display === 'status' ? 'Enterprise (Automation)' : 'Enterprise'
    case 'enterprise':
    case 'ent26':
    case 'enterprisecbpusagebased': return 'Enterprise'
    case 'edu': return display === 'status' ? 'Edu' : 'Education'
    case 'eduplus': return display === 'status' ? 'Edu Plus' : 'Education'
    case 'edupro': return display === 'status' ? 'Edu Pro' : 'Education'
    case 'unknown': return display === 'status' ? 'Unknown' : 'Account'
    default: return ''
  }
}
