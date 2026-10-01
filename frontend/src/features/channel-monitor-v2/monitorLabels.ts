type Translate = (key: string) => string
type Exists = (key: string) => boolean

function translatedOrValue(key: string, fallback: string, t: Translate, te: Exists): string {
  return te(key) ? t(key) : fallback
}

export function monitorSourceLabel(source: string | undefined, t: Translate, te: Exists): string {
  if (!source) return '—'
  return translatedOrValue(`channelMonitorV2.evidence.sources.${source}`, source, t, te)
}

export function monitorCategoryLabel(category: string, t: Translate, te: Exists): string {
  const [source, ...parts] = category.split(' · ')
  const key = parts.length ? parts.join(' · ') : source
  const label = translatedOrValue(`channelMonitorV2.errorCategories.${key}`, key, t, te)
  return parts.length ? `${monitorSourceLabel(source, t, te)} · ${label}` : label
}

export function monitorReasonLabel(reason: string, t: Translate, te: Exists): string {
  return translatedOrValue(`channelMonitorV2.unified.reasons.${reason}`, reason, t, te)
}

export function monitorOutcomeLabel(outcome: string, t: Translate, te: Exists): string {
  return translatedOrValue(`channelMonitorV2.unified.outcomes.${outcome}`, outcome, t, te)
}
