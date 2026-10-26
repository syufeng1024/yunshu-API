import type { Config } from 'tailwindcss'

// Every colour resolves to a CSS custom property declared in
// src/styles/tokens.css. Components must not carry raw hex values.
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        primary: {
          DEFAULT: 'var(--primary)',
          hover: 'var(--primary-hover)',
          strong: 'var(--primary-strong)',
          soft: 'var(--primary-soft)',
          ring: 'var(--primary-ring)',
        },
        canvas: 'var(--bg)',
        surface: {
          DEFAULT: 'var(--surface)',
          muted: 'var(--surface-2)',
        },
        border: {
          DEFAULT: 'var(--border)',
          strong: 'var(--border-strong)',
        },
        ink: {
          DEFAULT: 'var(--text)',
          secondary: 'var(--text-2)',
          muted: 'var(--text-3)',
          inverse: 'var(--text-inverse)',
        },
        success: {
          DEFAULT: 'var(--success)',
          soft: 'var(--success-soft)',
        },
        warning: {
          DEFAULT: 'var(--warning)',
          soft: 'var(--warning-soft)',
        },
        danger: {
          DEFAULT: 'var(--danger)',
          hover: 'var(--danger-hover)',
          soft: 'var(--danger-soft)',
        },
        info: {
          DEFAULT: 'var(--info)',
          soft: 'var(--info-soft)',
        },
        sidebar: {
          bg: 'var(--sidebar-bg)',
          surface: 'var(--sidebar-surface)',
          hover: 'var(--sidebar-hover)',
          active: 'var(--sidebar-active)',
          text: 'var(--sidebar-text)',
          muted: 'var(--sidebar-muted)',
          border: 'var(--sidebar-border)',
        },
        overlay: 'var(--overlay)',
      },
      fontFamily: {
        sans: ['var(--font-sans)'],
        mono: ['var(--font-mono)'],
      },
      fontSize: {
        xs: ['var(--font-size-xs)', { lineHeight: 'var(--line-height-xs)' }],
        sm: ['var(--font-size-sm)', { lineHeight: 'var(--line-height-sm)' }],
        base: ['var(--font-size-base)', { lineHeight: 'var(--line-height-base)' }],
        lg: ['var(--font-size-lg)', { lineHeight: 'var(--line-height-lg)' }],
        xl: ['var(--font-size-xl)', { lineHeight: 'var(--line-height-xl)' }],
        '2xl': ['var(--font-size-2xl)', { lineHeight: 'var(--line-height-2xl)' }],
        '3xl': ['var(--font-size-3xl)', { lineHeight: 'var(--line-height-3xl)' }],
        wordmark: [
          'var(--font-size-wordmark)',
          { lineHeight: '1', letterSpacing: '-0.02em', fontWeight: '600' },
        ],
        'wordmark-sm': [
          'var(--font-size-wordmark-sm)',
          { lineHeight: '1', letterSpacing: '-0.02em', fontWeight: '600' },
        ],
      },
      borderRadius: {
        xs: 'var(--radius-xs)',
        sm: 'var(--radius-sm)',
        md: 'var(--radius-md)',
        lg: 'var(--radius-lg)',
        xl: 'var(--radius-xl)',
        mark: 'var(--logo-radius)',
        'mark-sm': 'var(--logo-radius-sm)',
      },
      spacing: {
        mark: 'var(--logo-mark)',
        'mark-sm': 'var(--logo-mark-sm)',
        'mark-icon': 'var(--logo-icon)',
        'brand-gap': 'var(--logo-gap)',
        'brand-gap-dark': 'var(--logo-gap-dark)',
        sidebar: 'var(--sidebar-width)',
        'sidebar-collapsed': 'var(--sidebar-collapsed-width)',
      },
      boxShadow: {
        card: 'var(--shadow-sm)',
        pop: 'var(--shadow-md)',
        overlay: 'var(--shadow-lg)',
      },
      transitionDuration: {
        fast: '120ms',
      },
    },
  },
  plugins: [],
} satisfies Config
