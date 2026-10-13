import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { LineElement, PointElement, type Chart, type ChartData, type ChartOptions, type Plugin, type ScriptableContext, type TooltipItem, type TooltipModel } from 'chart.js'

import TrendLineChart from '../TrendLineChart.vue'

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options', 'plugins'],
    template: '<canvas />',
  },
}))

enableAutoUnmount(afterEach)

function mockReducedMotion(reduced: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation(query => ({
    matches: query === '(prefers-reduced-motion: reduce)' && reduced,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

function chartData(): ChartData<'line'> {
  return {
    labels: ['21:20', '21:21', '21:22'],
    datasets: [
      { label: 'Requests', data: [12, null, 0], yAxisID: 'requests', borderColor: '#10b981' },
      { label: 'Latency', data: [55, 13, 26], yAxisID: 'latency', borderColor: '#0ea5e9', fill: false },
    ],
  }
}

function optionsOf(wrapper: VueWrapper): ChartOptions<'line'> {
  return wrapper.findComponent({ name: 'Line' }).props('options')
}

function tooltipPoint(datasetIndex: number, value: number | null, x = 100): TooltipItem<'line'> {
  const data = chartData()
  return {
    datasetIndex,
    dataIndex: 0,
    label: '21:20',
    formattedValue: String(value),
    raw: value,
    parsed: { x: 0, y: value },
    dataset: data.datasets[datasetIndex],
    element: { getProps: () => ({ x, y: datasetIndex ? 60 : 90 }) },
  } as unknown as TooltipItem<'line'>
}

async function showTooltip(wrapper: VueWrapper, points = [tooltipPoint(0, 12)], overrides: Partial<TooltipModel<'line'>> = {}, chartOverrides: Partial<Chart<'line'>> = {}) {
  // Chart.js supplies a model after applying caller callbacks and sorting. The
  // renderer must preserve that model rather than formatting raw values again.
  const tooltip = {
    opacity: 1,
    title: ['21:20'],
    beforeBody: [],
    afterBody: [],
    footer: [],
    dataPoints: points,
    body: points.map(point => ({ before: [], lines: [`${point.dataset.label}: ${point.formattedValue}`], after: [] })),
    ...overrides,
  } as unknown as TooltipModel<'line'>
  const chart = {
    width: 600,
    height: 240,
    chartArea: { top: 20, bottom: 220, left: 0, right: 600, width: 600, height: 200 },
    ...chartOverrides,
  } as Chart<'line'>
  const external = optionsOf(wrapper).plugins?.tooltip?.external
  expect(typeof external).toBe('function')
  external!.call(tooltip, { chart, tooltip })
  await nextTick()
  return { chart, tooltip }
}

function continuousHoverPlugin(wrapper: VueWrapper): Plugin<'line'> {
  const plugins: Plugin<'line'>[] = wrapper.findComponent({ name: 'Line' }).props('plugins')
  const plugin = plugins.find(item => item.id === 'trend-continuous-hover')
  expect(plugin).toBeDefined()
  return plugin!
}

async function moveCursor(plugin: Plugin<'line'>, chart: Chart<'line'>, x: number, y = 120, inChartArea = true) {
  plugin.afterEvent!(chart, {
    event: { type: 'mousemove', x, y, native: null },
    inChartArea,
    replay: false,
    changed: false,
  }, {})
  await nextTick()
}

function renderedLine(values: (number | null)[]) {
  const points = values.map((value, index) => new PointElement({
    x: 100 + index * 100,
    y: value == null ? Number.NaN : 150 - value,
    skip: value == null,
  }))
  const dataset = new LineElement({
    points,
    options: { tension: 0, cubicInterpolationMode: 'default', stepped: false, spanGaps: false },
    _loop: false,
    _fullLoop: false,
  })
  return { data: points, dataset }
}

function tooltipBox(wrapper: VueWrapper) {
  const element = wrapper.get('.trend-tooltip').element as HTMLElement
  const horizontalOffset = /^translate\((-?[\d.]+)px,/.exec(element.style.transform)
  expect(horizontalOffset).not.toBeNull()
  return { left: Number(horizontalOffset![1]), width: Number.parseFloat(element.style.width) }
}

async function hoverAtWidth(wrapper: VueWrapper, width: number, points: TooltipItem<'line'>[], metas: ReturnType<typeof renderedLine>[]) {
  const { chart, tooltip } = await showTooltip(wrapper, points, {}, {
    width,
    chartArea: { top: 20, bottom: 220, left: 0, right: width, width, height: 200 },
    getDatasetMeta: ((index: number) => metas[index]) as unknown as Chart<'line'>['getDatasetMeta'],
  })
  Object.assign(chart, { tooltip })
  return { chart, plugin: continuousHoverPlugin(wrapper) }
}

describe('TrendLineChart', () => {
  beforeEach(() => mockReducedMotion(false))
  afterEach(() => vi.restoreAllMocks())

  it('preserves samples, missing buckets, series order, secondary axes and unfilled series', () => {
    const data = chartData()
    const original = structuredClone(data)
    const wrapper = mount(TrendLineChart, { props: { data } })
    const rendered: ChartData<'line'> = wrapper.findComponent({ name: 'Line' }).props('data')

    expect(rendered.labels).toEqual(original.labels)
    expect(rendered.datasets.map(({ label, data, yAxisID, fill }) => ({ label, data, yAxisID, fill }))).toEqual([
      { label: 'Requests', data: [12, null, 0], yAxisID: 'requests', fill: 'origin' },
      { label: 'Latency', data: [55, 13, 26], yAxisID: 'latency', fill: false },
    ])
    expect(rendered.datasets.every(dataset => dataset.spanGaps === false)).toBe(true)
    expect(data).toEqual(original)
  })

  it('keeps an isolated zero sample visible without drawing across missing buckets', () => {
    const wrapper = mount(TrendLineChart, { props: {
      data: { labels: ['21:20', '21:21', '21:22'], datasets: [{ label: 'Requests', data: [null, 0, null] }] },
    } })
    const rendered: ChartData<'line'> = wrapper.findComponent({ name: 'Line' }).props('data')
    const dataset = rendered.datasets[0]
    const radius = dataset.pointRadius as (context: ScriptableContext<'line'>) => number

    expect(dataset.data).toEqual([null, 0, null])
    expect(radius({ dataIndex: 1 } as ScriptableContext<'line'>)).toBeGreaterThan(0)
    expect(radius({ dataIndex: 0 } as ScriptableContext<'line'>)).toBe(0)
    expect(radius({ dataIndex: 2 } as ScriptableContext<'line'>)).toBe(0)
    expect(dataset.spanGaps).toBe(false)
  })

  it('keeps caller units, limits, tooltip callbacks and sorting while displaying the formatted model', async () => {
    const footer = vi.fn(() => 'Actual cost: $1.20 | Standard: $2.40')
    const label = vi.fn((point: TooltipItem<'line'>) => `${point.dataset.label}: ${point.parsed.y} ms`)
    const itemSort = (a: TooltipItem<'line'>, b: TooltipItem<'line'>) => b.datasetIndex - a.datasetIndex
    const tickLabel = (value: string | number) => `${value}%`
    const options: ChartOptions<'line'> = {
      scales: { latency: { position: 'right', min: 0, max: 100, ticks: { callback: tickLabel } } },
      plugins: { tooltip: { callbacks: { footer, label }, itemSort } },
    }
    const wrapper = mount(TrendLineChart, { props: { data: chartData(), options } })
    const rendered = optionsOf(wrapper)

    expect(rendered.scales?.latency).toMatchObject({ position: 'right', min: 0, max: 100 })
    expect(rendered.scales?.latency?.ticks?.callback).toBe(tickLabel)
    expect(rendered.plugins?.tooltip?.callbacks?.footer).toBe(footer)
    expect(rendered.plugins?.tooltip?.callbacks?.label).toBe(label)
    expect(rendered.plugins?.tooltip?.itemSort).toBe(itemSort)

    const points = [tooltipPoint(0, 12), tooltipPoint(1, 55)].sort(itemSort)
    await showTooltip(wrapper, points, {
      beforeBody: ['Summary'],
      body: points.map(point => ({ before: [], lines: [label(point)], after: [] })),
      afterBody: ['Measured values'],
      footer: [footer()],
    })
    const text = wrapper.get('.trend-tooltip').text()
    expect(text).toContain('21:20')
    expect(text).toContain('Summary')
    expect(text).toContain('Latency55 ms')
    expect(text.indexOf('Latency')).toBeLessThan(text.indexOf('Requests'))
    expect(text).toContain('Measured values')
    expect(text).toContain('Actual cost: $1.20 | Standard: $2.40')
  })

  it('updates the same hover elements between buckets and renders callback text safely', async () => {
    const wrapper = mount(TrendLineChart, { props: { data: chartData() } })
    await showTooltip(wrapper)
    const crosshair = wrapper.get('.trend-crosshair').element
    const tooltip = wrapper.get('.trend-tooltip').element
    const firstTransform = (crosshair as HTMLElement).style.transform

    await showTooltip(wrapper, [tooltipPoint(0, 20, 220)], { title: ['<img src=x onerror=alert(1)>'] })

    expect(wrapper.get('.trend-crosshair').element).toBe(crosshair)
    expect(wrapper.get('.trend-tooltip').element).toBe(tooltip)
    expect((crosshair as HTMLElement).style.transform).not.toBe(firstTransform)
    expect(wrapper.get('.trend-tooltip').text()).toContain('20')
    expect(wrapper.get('.trend-tooltip').text()).toContain('<img src=x onerror=alert(1)>')
    expect(wrapper.find('.trend-tooltip img').exists()).toBe(false)
  })

  it('hides stale hover on leaving the plot and clears it when data is replaced', async () => {
    const wrapper = mount(TrendLineChart, { props: { data: chartData() } })
    await showTooltip(wrapper)
    expect(wrapper.get('.trend-hover').classes()).toContain('is-visible')

    await wrapper.trigger('mouseleave')
    expect(wrapper.get('.trend-hover').classes()).not.toContain('is-visible')

    await showTooltip(wrapper)
    await wrapper.setProps({ data: { labels: ['22:00'], datasets: [{ label: 'New range', data: [8] }] } })
    expect(wrapper.find('.trend-hover').exists()).toBe(false)
  })

  it('does not leave a highlighted point over an empty bucket or a dismissed tooltip', async () => {
    const wrapper = mount(TrendLineChart, { props: { data: chartData() } })
    await showTooltip(wrapper)
    await showTooltip(wrapper, [tooltipPoint(0, null)])
    expect(wrapper.get('.trend-hover').classes()).not.toContain('is-visible')

    await showTooltip(wrapper)
    await showTooltip(wrapper, [], { opacity: 0 })
    expect(wrapper.get('.trend-hover').classes()).not.toContain('is-visible')
  })

  it('moves the marker along the rendered line within one date bucket while keeping tooltip values at that date', async () => {
    const data: ChartData<'line'> = {
      labels: ['21:20', '21:21', '21:22'],
      datasets: [{ label: 'Requests', data: [10, 50, 20] }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 50, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, tooltip } = await showTooltip(wrapper, [point], {}, {
      getDatasetMeta: (() => meta) as unknown as Chart<'line'>['getDatasetMeta'],
    })
    Object.assign(chart, { tooltip })
    const plugin = continuousHoverPlugin(wrapper)
    const marker = wrapper.get('.trend-point').element

    await moveCursor(plugin, chart, 120)
    expect((marker as HTMLElement).style.transform).toBe('translate(115px, 127px)')
    expect(wrapper.get('.trend-crosshair').attributes('style')).toContain('translateX(120px)')

    await moveCursor(plugin, chart, 140)
    expect(wrapper.get('.trend-point').element).toBe(marker)
    expect((marker as HTMLElement).style.transform).toBe('translate(135px, 119px)')
    expect(wrapper.get('.trend-crosshair').attributes('style')).toContain('translateX(140px)')
    expect(wrapper.get('.trend-tooltip').text()).toBe('21:20Requests10')
    expect(tooltip.dataPoints[0].dataIndex).toBe(0)

    // Chart animation may move the rendered curve without a new pointer event.
    meta.data[0].y = 150
    meta.data[1].y = 110
    plugin.afterDraw!(chart, {}, {})
    await nextTick()
    expect((marker as HTMLElement).style.transform).toBe('translate(135px, 129px)')
    expect(wrapper.get('.trend-tooltip').text()).toBe('21:20Requests10')
  })

  it.each([
    { name: 'missing bucket', values: [10, null, 30], sampleIndex: 0 },
    { name: 'isolated zero', values: [null, 0, null], sampleIndex: 1 },
    { name: 'single sample', values: [0], sampleIndex: 0 },
  ])('keeps $name hover on its real sample instead of inventing a curve', async ({ values, sampleIndex }) => {
    const data: ChartData<'line'> = {
      labels: values.map((_, index) => `21:2${index}`),
      datasets: [{ label: 'Requests', data: values }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine(values)
    const point = {
      ...tooltipPoint(0, values[sampleIndex]),
      dataIndex: sampleIndex,
      dataset: data.datasets[0],
      element: meta.data[sampleIndex],
    }
    const { chart, tooltip } = await showTooltip(wrapper, [point], {}, {
      getDatasetMeta: (() => meta) as unknown as Chart<'line'>['getDatasetMeta'],
    })
    Object.assign(chart, { tooltip })
    const plugin = continuousHoverPlugin(wrapper)
    const originalPosition = wrapper.get('.trend-point').attributes('style')
    const originalCrosshair = wrapper.get('.trend-crosshair').attributes('style')

    await moveCursor(plugin, chart, 150)
    await moveCursor(plugin, chart, 175)

    expect(wrapper.get('.trend-point').attributes('style')).toBe(originalPosition)
    expect(wrapper.get('.trend-crosshair').attributes('style')).toBe(originalCrosshair)
    expect(wrapper.get('.trend-tooltip').text()).toContain(String(values[sampleIndex]))
    expect(wrapper.get('.trend-point').classes()).not.toContain('is-continuous')
  })

  it('falls back to the sample when Chart.js marks a rendered point as skipped', async () => {
    const data: ChartData<'line'> = { labels: ['21:20', '21:21'], datasets: [{ label: 'Requests', data: [10, 20] }] }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 20])
    meta.data[1].skip = true
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, tooltip } = await showTooltip(wrapper, [point], {}, {
      getDatasetMeta: (() => meta) as unknown as Chart<'line'>['getDatasetMeta'],
    })
    Object.assign(chart, { tooltip })

    await moveCursor(continuousHoverPlugin(wrapper), chart, 150)

    expect(wrapper.get('.trend-point').attributes('style')).toContain('translate(95px, 135px)')
    expect(wrapper.get('.trend-point').classes()).not.toContain('is-continuous')
  })

  it('hides continuous hover outside the chart area and does not revive it during redraw', async () => {
    const data: ChartData<'line'> = { labels: ['21:20', '21:21'], datasets: [{ label: 'Requests', data: [10, 20] }] }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 20])
    const { chart, tooltip } = await showTooltip(wrapper, [tooltipPoint(0, 10)], {}, {
      getDatasetMeta: (() => meta) as unknown as Chart<'line'>['getDatasetMeta'],
    })
    Object.assign(chart, { tooltip })
    const plugin = continuousHoverPlugin(wrapper)
    await moveCursor(plugin, chart, 150)
    expect(wrapper.get('.trend-hover').classes()).toContain('is-visible')

    await moveCursor(plugin, chart, 150, 0, false)
    plugin.afterDraw!(chart, {}, {})
    await nextTick()

    expect(wrapper.get('.trend-hover').classes()).not.toContain('is-visible')
  })

  it.each([300, 400])('uses a smooth fallback in a %ipx chart when neither side has room', async width => {
    const data: ChartData<'line'> = {
      labels: ['21:20', '21:21', '21:22'],
      datasets: [{ label: 'Requests', data: [10, 50, 20] }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 50, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, width, [point], [meta])
    const cursorPositions = [width / 2 - 10, width / 2 - 2, width / 2 + 2, width / 2 + 10]
    const boxPositions: number[] = []

    for (const cursor of cursorPositions) {
      await moveCursor(plugin, chart, cursor)
      boxPositions.push(tooltipBox(wrapper).left)
      expect(wrapper.get('.trend-tooltip').text()).toBe('21:20Requests10')
    }

    for (let i = 1; i < boxPositions.length; i++) {
      // The box follows the pointer within a bucket, including crossing the
      // chart's midpoint: no stationary plateau or sudden opposite-side jump.
      const movement = boxPositions[i] - boxPositions[i - 1]
      expect(movement).toBeGreaterThan(0)
      expect(movement).toBeCloseTo(cursorPositions[i] - cursorPositions[i - 1])
    }
  })

  it('keeps the tooltip beside the pointer at narrow chart edges and moves smoothly through its interior', async () => {
    const data: ChartData<'line'> = {
      labels: ['21:20', '21:21', '21:22'],
      datasets: [{ label: 'Requests', data: [10, 50, 20] }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 50, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, 400, [point], [meta])
    const positions: { left: number; width: number }[] = []

    for (const cursor of [5, 10, 180, 220, 390, 395]) {
      await moveCursor(plugin, chart, cursor)
      const box = tooltipBox(wrapper)
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.left + box.width).toBeLessThanOrEqual(chart.width)
      positions.push(box)
    }

    expect(positions[0].left - 5).toBe(16)
    expect(positions[1].left - positions[0].left).toBe(5)
    expect(positions[3].left - positions[2].left).toBeCloseTo(40)
    expect(390 - positions[4].left - positions[4].width).toBe(16)
    expect(positions[5].left - positions[4].left).toBe(5)
  })

  it.each([558, 734])('stays beside the cursor with a gap in a %ipx chart and does not flip at the midpoint', async width => {
    const data: ChartData<'line'> = {
      labels: ['21:20', '21:21', '21:22'],
      datasets: [{ label: 'Requests', data: [10, 50, 20] }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 50, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, width, [point], [meta])
    const rightPositions: number[] = []

    for (const cursor of [width / 2 - 10, width / 2 + 10]) {
      await moveCursor(plugin, chart, cursor)
      const box = tooltipBox(wrapper)
      expect(box.left - cursor).toBe(16)
      expect(box.left + box.width).toBeLessThanOrEqual(width)
      rightPositions.push(box.left)
    }
    expect(rightPositions[1] - rightPositions[0]).toBe(20)

    const cursorNearRightEdge = width - 40
    await moveCursor(plugin, chart, cursorNearRightEdge)
    const leftBox = tooltipBox(wrapper)
    expect(cursorNearRightEdge - leftBox.left - leftBox.width).toBe(16)
    expect(leftBox.left).toBeGreaterThanOrEqual(0)
    expect(wrapper.get('.trend-tooltip').text()).toBe('21:20Requests10')
  })

  it.each([
    { width: 558, lastRightCursor: 318 },
    { width: 734, lastRightCursor: 494 },
  ])('changes sides in a $width px chart only when the right side no longer fits', async ({ width, lastRightCursor }) => {
    const data: ChartData<'line'> = { labels: ['21:20', '21:21'], datasets: [{ label: 'Requests', data: [10, 20] }] }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, width, [point], [meta])

    await moveCursor(plugin, chart, lastRightCursor - 1)
    const beforeEdge = tooltipBox(wrapper)
    await moveCursor(plugin, chart, lastRightCursor)
    const atEdge = tooltipBox(wrapper)
    expect(beforeEdge.left).toBeGreaterThan(lastRightCursor - 1)
    expect(atEdge.left - beforeEdge.left).toBe(1)
    expect(atEdge.left + atEdge.width).toBe(width)

    await moveCursor(plugin, chart, lastRightCursor + 1)
    const onLeft = tooltipBox(wrapper)
    expect(lastRightCursor + 1 - onLeft.left - onLeft.width).toBe(16)
    expect(onLeft.left).toBeGreaterThanOrEqual(0)

    await moveCursor(plugin, chart, lastRightCursor + 21)
    expect(tooltipBox(wrapper).left - onLeft.left).toBe(20)
  })

  it.each([600, 400])('keeps all seven series visible within a %ipx chart while moving only where the tooltip fits', async width => {
    const data: ChartData<'line'> = {
      labels: ['21:20', '21:21'],
      datasets: Array.from({ length: 7 }, (_, index) => ({ label: `Series ${index}`, data: [index, index + 10] })),
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const metas = data.datasets.map(dataset => renderedLine(dataset.data as number[]))
    const points = data.datasets.map((dataset, index) => ({
      ...tooltipPoint(0, index), datasetIndex: index, dataset, element: metas[index].data[0],
    }))
    const { chart, plugin } = await hoverAtWidth(wrapper, width, points, metas)
    const positions: number[] = []

    for (const cursor of [width / 2 - 10, width / 2 + 10]) {
      await moveCursor(plugin, chart, cursor)
      const box = tooltipBox(wrapper)
      expect(box.width).toBe(400)
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.left + box.width).toBeLessThanOrEqual(width)
      for (let index = 0; index < 7; index++) expect(wrapper.get('.trend-tooltip').text()).toContain(`Series ${index}`)
      positions.push(box.left)
    }

    if (width > 400) expect(positions[1] - positions[0]).toBeCloseTo(20)
    else expect(positions).toEqual([0, 0])
  })

  it.each([
    { name: 'sparse data', values: [10, null, 30] },
    { name: 'single zero sample', values: [0] },
  ])('lets the tooltip follow the pointer over $name while its marker stays on the real sample', async ({ values }) => {
    const data: ChartData<'line'> = {
      labels: values.map((_, index) => `21:2${index}`),
      datasets: [{ label: 'Requests', data: values }],
    }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine(values)
    const point = { ...tooltipPoint(0, values[0]), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, 400, [point], [meta])
    const markerPosition = wrapper.get('.trend-point').attributes('style')
    const crosshairPosition = wrapper.get('.trend-crosshair').attributes('style')
    const text = wrapper.get('.trend-tooltip').text()

    await moveCursor(plugin, chart, 170)
    const first = tooltipBox(wrapper)
    await moveCursor(plugin, chart, 190)
    const second = tooltipBox(wrapper)

    expect(second.left - first.left).toBeCloseTo(20)
    expect(wrapper.get('.trend-point').attributes('style')).toBe(markerPosition)
    expect(wrapper.get('.trend-crosshair').attributes('style')).toBe(crosshairPosition)
    expect(wrapper.get('.trend-point').classes()).not.toContain('is-continuous')
    expect(wrapper.get('.trend-tooltip').text()).toBe(text)
  })

  it('keeps a tall tooltip at its measured vertical position during subsequent pointer moves', async () => {
    const measuredTransforms: string[] = []
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
      if (!this.classList.contains('trend-tooltip')) return 0
      // Reading layout commits the current transform in a browser. A temporary
      // vertical reset here would restart horizontal CSS motion on every event.
      measuredTransforms.push(this.style.transform)
      return 250
    })
    const data: ChartData<'line'> = { labels: ['21:20', '21:21'], datasets: [{ label: 'Requests', data: [10, 20] }] }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, 400, [point], [meta])
    await nextTick()
    expect((wrapper.get('.trend-tooltip').element as HTMLElement).style.transform).toMatch(/, 0px\)$/)
    measuredTransforms.length = 0

    for (const cursor of [170, 190, 210]) {
      await moveCursor(plugin, chart, cursor)
      await nextTick()
    }

    expect(measuredTransforms.length).toBeGreaterThan(0)
    expect(measuredTransforms.every(transform => /, 0px\)$/.test(transform))).toBe(true)
    const horizontalPositions = measuredTransforms.map(transform => Number(/^translate\((-?[\d.]+)px,/.exec(transform)![1]))
    expect(horizontalPositions[horizontalPositions.length - 1]).toBeGreaterThan(horizontalPositions[0])
    expect(wrapper.get('.trend-tooltip').text()).toBe('21:20Requests10')
  })

  it('remeasures vertical placement when tooltip content grows and shrinks', async () => {
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
      if (!this.classList.contains('trend-tooltip')) return 0
      return this.textContent?.includes('Expanded footer') ? 250 : 120
    })
    const data: ChartData<'line'> = { labels: ['21:20', '21:21'], datasets: [{ label: 'Requests', data: [10, 20] }] }
    const wrapper = mount(TrendLineChart, { props: { data } })
    const meta = renderedLine([10, 20])
    const point = { ...tooltipPoint(0, 10), dataset: data.datasets[0], element: meta.data[0] }
    const { chart, plugin } = await hoverAtWidth(wrapper, 400, [point], [meta])
    await nextTick()
    const element = wrapper.get('.trend-tooltip').element as HTMLElement
    const initialVerticalPosition = element.style.transform.split(', ')[1]

    chart.tooltip!.footer = ['Expanded footer']
    await moveCursor(plugin, chart, 140)
    await nextTick()
    expect(wrapper.get('.trend-tooltip').text()).toContain('Expanded footer')
    expect(element.style.transform).toMatch(/, 0px\)$/)
    expect(element.style.transform.split(', ')[1]).not.toBe(initialVerticalPosition)

    chart.tooltip!.footer = []
    await moveCursor(plugin, chart, 160)
    await nextTick()
    expect(wrapper.get('.trend-tooltip').text()).not.toContain('Expanded footer')
    expect(element.style.transform.split(', ')[1]).toBe(initialVerticalPosition)
  })

  it('enables animation when motion is allowed', async () => {
    const wrapper = mount(TrendLineChart, { props: { data: chartData() } })
    await nextTick()
    expect(optionsOf(wrapper).animation).not.toBe(false)
    expect(wrapper.classes()).not.toContain('trend-motion-off')
  })

  it.each([
    { motion: false, reduced: false },
    { motion: true, reduced: true },
  ])('disables chart and hover motion for motion=$motion, reduced=$reduced', async ({ motion, reduced }) => {
    mockReducedMotion(reduced)
    const wrapper = mount(TrendLineChart, { props: { data: chartData(), motion } })
    await nextTick()
    expect(optionsOf(wrapper).animation).toBe(false)
    expect(wrapper.classes()).toContain('trend-motion-off')
  })
})
