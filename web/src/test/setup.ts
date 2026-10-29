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

/**
 * jsdom implements `window.scrollTo` only as "not implemented", which logs a
 * jsdom error every time a data router's <ScrollRestoration /> mounts (T29).
 * Those log lines surface as a non-zero vitest "Errors" count and make the
 * shared worker slower and noisier, so the call is stubbed here. Component
 * tests that need to observe scroll calls install their own spy
 * (see ScrollRestorer.test.tsx).
 */
window.scrollTo = (() => {}) as typeof window.scrollTo
