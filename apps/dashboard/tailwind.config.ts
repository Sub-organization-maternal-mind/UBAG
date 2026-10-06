import type { Config } from 'tailwindcss';

export default {
  content: [
    './src/**/*.{html,js,svelte,ts}',
  ],
  theme: {
    extend: {
      // Values must mirror src/app.css. The `/ <alpha-value>` placeholder is
      // required: without it Tailwind silently drops every opacity-modified
      // utility (bg-ink/30, backdrop:bg-ink/40, …) for oklch string colors.
      colors: {
        paper: 'oklch(96.5% 0.012 75 / <alpha-value>)',
        'paper-soft': 'oklch(99% 0.006 75 / <alpha-value>)',
        'paper-warm': 'oklch(93% 0.02 70 / <alpha-value>)',
        ink: 'oklch(20% 0.022 55 / <alpha-value>)',
        'ink-soft': 'oklch(38% 0.018 55 / <alpha-value>)',
        'ink-mute': 'oklch(46% 0.012 60 / <alpha-value>)',
        rule: 'oklch(86% 0.014 70 / <alpha-value>)',
        'rule-soft': 'oklch(91% 0.01 70 / <alpha-value>)',
        accent: 'oklch(58% 0.18 35 / <alpha-value>)',
        'accent-deep': 'oklch(42% 0.2 32 / <alpha-value>)',
        'accent-soft': 'oklch(82% 0.08 45 / <alpha-value>)',
        saffron: 'oklch(78% 0.16 78 / <alpha-value>)',
        'saffron-soft': 'oklch(91% 0.07 80 / <alpha-value>)',
        marine: 'oklch(34% 0.09 240 / <alpha-value>)',
        'marine-soft': 'oklch(83% 0.045 240 / <alpha-value>)',
        success: 'oklch(46% 0.09 150 / <alpha-value>)',
        'success-soft': 'oklch(90% 0.04 145 / <alpha-value>)',
        warning: 'oklch(48% 0.13 70 / <alpha-value>)',
        'warning-soft': 'oklch(90% 0.06 75 / <alpha-value>)',
        danger: 'oklch(48% 0.17 25 / <alpha-value>)',
        'danger-soft': 'oklch(89% 0.055 32 / <alpha-value>)',
        'focus-ring': 'oklch(48% 0.2 32 / <alpha-value>)',
      },
      fontFamily: {
        display: ['ui-rounded', 'Aptos Display', 'Segoe UI', 'system-ui', 'sans-serif'],
        body: ['Aptos', 'Segoe UI', 'BlinkMacSystemFont', 'sans-serif'],
        mono: ['Cascadia Mono', 'SFMono-Regular', 'Consolas', 'ui-monospace', 'monospace'],
      },
      borderRadius: {
        sm: '4px',
        md: '8px',
        lg: '12px',
        pill: '999px',
      },
    },
  },
  // The Skeleton preset plugin was removed: no Skeleton preset classes are
  // used anywhere in src/ (the design system is fully custom in app.css).
} satisfies Config;
