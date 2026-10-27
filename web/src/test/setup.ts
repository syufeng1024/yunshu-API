import '@testing-library/jest-dom/vitest'

/**
 * jsdom has no ResizeObserver, which recharts' ResponsiveContainer requires at
 * mount. A no-op implementation lets chart-bearing pages render; the chart's
 * SVG output is not asserted in unit tests.
 */
if (typeof globalThis.ResizeObserver === 'undefined') {
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver
}
