import { describe, expect, it, beforeEach, afterEach } from 'vitest'

import { externalTooltipHandler } from '../chartExternalTooltip'

describe('externalTooltipHandler', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('matches the native Chart.js tooltip structure and styling outside the canvas', () => {
    const canvas = document.createElement('canvas')
    document.body.appendChild(canvas)

    externalTooltipHandler({
      chart: {
        canvas,
        width: 192,
        height: 192,
      } as any,
      tooltip: {
        opacity: 1,
        title: ['model-a'],
        body: [{ lines: ['model-a: 1.20K (66.7%)'] }],
        footer: ['Actual: $0.01 | Standard: $0.02'],
        labelColors: [{ backgroundColor: '#3b82f6', borderColor: '#3b82f6' }],
        caretX: 96,
        caretY: 96,
      } as any,
    })

    const tooltip = document.getElementById('token-router-chart-tooltip')
    const viewport = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-viewport]')
    const panel = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-panel]')
    const title = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-title]')
    const swatch = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-swatch]')
    const footer = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-footer]')
    const caret = tooltip?.querySelector<HTMLElement>('[data-chart-tooltip-caret]')
    expect(tooltip).toBeTruthy()
    expect(tooltip?.textContent).toContain('model-a: 1.20K (66.7%)')
    expect(tooltip?.classList.contains('hidden')).toBe(false)
    expect(tooltip?.style.position).toBe('fixed')
    expect(tooltip?.style.transition).toContain('left 400ms cubic-bezier(0.25, 1, 0.5, 1)')
    expect(tooltip?.style.transition).toContain('width 400ms cubic-bezier(0.25, 1, 0.5, 1)')
    expect(tooltip?.style.transition).toContain('opacity 200ms linear')
    expect(tooltip?.getAttribute('aria-hidden')).toBe('false')
    expect(viewport?.style.overflow).toBe('hidden')
    expect(panel?.parentElement).toBe(viewport)
    expect(panel?.style.backgroundColor).toBe('rgba(0, 0, 0, 0.8)')
    expect(panel?.style.borderWidth).toBe('0px')
    expect(panel?.style.borderRadius).toBe('6px')
    expect(panel?.style.padding).toBe('6px')
    expect(panel?.style.boxShadow).toBe('none')
    expect(title?.textContent).toBe('model-a')
    expect(title?.style.fontWeight).toBe('bold')
    expect(title?.style.marginBottom).toBe('6px')
    expect(footer?.textContent).toBe('Actual: $0.01 | Standard: $0.02')
    expect(footer?.style.fontWeight).toBe('bold')
    expect(footer?.style.marginTop).toBe('6px')
    expect(swatch?.style.width).toBe('12px')
    expect(swatch?.style.height).toBe('12px')
    expect(swatch?.style.borderRadius).toBe('0px')
    expect(caret?.style.borderTop).toContain('5px solid rgba(0, 0, 0, 0.8)')
  })

  it('hides the shared tooltip when Chart.js reports zero opacity', () => {
    const canvas = document.createElement('canvas')
    document.body.appendChild(canvas)

    externalTooltipHandler({
      chart: { canvas, width: 192, height: 192 } as any,
      tooltip: {
        opacity: 1,
        title: [],
        body: [{ lines: ['value'] }],
        caretX: 10,
        caretY: 10,
      } as any,
    })
    externalTooltipHandler({
      chart: { canvas, width: 192, height: 192 } as any,
      tooltip: { opacity: 0 } as any,
    })

    const tooltip = document.getElementById('token-router-chart-tooltip')
    expect(tooltip?.style.opacity).toBe('0')
    expect(tooltip?.getAttribute('aria-hidden')).toBe('true')
  })
})
