import { describe, expect, it } from 'vitest'
import { median, summarizeMonitorTraffic } from '../monitorSummary'
import { getMonitorPreviewScenario } from '../monitorPreview'

describe('traffic headline semantics', () => {
  it('computes the median for even samples without averaging request percentiles', () => {
    expect(median([790, 640, null, undefined, NaN])).toBe(715)
    expect(median([])).toBeNull()
  })

  it('does not require private request counters on a user projection', () => {
    const overview = getMonitorPreviewScenario('normal', 'user').overview!
    for (const item of overview.items) {
      item.metrics.request_count = 0
      item.metrics.success_requests = 0
      item.metrics.channel_errors = 0
    }
    const summary = summarizeMonitorTraffic(overview)
    expect(summary.healthy).toBe(2)
    expect(summary.healthRate).toBe(1)
    expect(summary.sources[0].ttftP50).toBe(715)
    expect(summary.sources[0].errorRate).toBeGreaterThan(0)
  })

  it('separates source populations and excludes latency and insufficient traffic from health', () => {
    const overview = getMonitorPreviewScenario('normal', 'admin').overview!
    overview.source = 'mixed'
    overview.items[1].source = 'legacy'
    overview.items[1].health.latency = 'critical'
    const summary = summarizeMonitorTraffic(overview)
    expect(summary.healthRate).toBe(.5)
    expect(summary.sources.map(source => source.source)).toEqual(['compact', 'legacy'])
    ;(overview.items[0].traffic || overview.items[0].metrics).sample_state = 'insufficient'
    expect(summarizeMonitorTraffic(overview).healthy).toBe(0)
  })

  it('never turns a healthy probe or unavailable coverage into healthy traffic', () => {
    const overview = getMonitorPreviewScenario('probe-no-traffic', 'admin').overview!
    expect(summarizeMonitorTraffic(overview).healthy).toBe(0)
    const complete = getMonitorPreviewScenario('normal', 'admin').overview!
    complete.coverage.state = 'unavailable'
    expect(summarizeMonitorTraffic(complete).healthRate).toBeNull()
  })
})
