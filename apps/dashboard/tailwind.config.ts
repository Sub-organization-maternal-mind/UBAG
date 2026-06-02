import { join } from 'path';
import type { Config } from 'tailwindcss';
import { skeleton } from '@skeletonlabs/tw-plugin';

export default {
  darkMode: 'class',
  content: [
    './src/**/*.{html,js,svelte,ts}',
    join(require.resolve('@skeletonlabs/skeleton'), '../**/*.{html,js,svelte,ts}'),
  ],
  theme: {
    extend: {
      colors: {
        paper: 'oklch(96.5% 0.012 75)',
        'paper-soft': 'oklch(99% 0.006 75)',
        'paper-warm': 'oklch(93% 0.02 70)',
        ink: 'oklch(20% 0.022 55)',
        'ink-soft': 'oklch(38% 0.018 55)',
        'ink-mute': 'oklch(56% 0.012 60)',
        rule: 'oklch(86% 0.014 70)',
        'rule-soft': 'oklch(91% 0.01 70)',
        accent: 'oklch(58% 0.18 35)',
        'accent-deep': 'oklch(48% 0.2 32)',
        'accent-soft': 'oklch(82% 0.08 45)',
        saffron: 'oklch(78% 0.16 78)',
        'saffron-soft': 'oklch(91% 0.07 80)',
        marine: 'oklch(34% 0.09 240)',
        'marine-soft': 'oklch(83% 0.045 240)',
        success: 'oklch(50% 0.09 150)',
        'success-soft': 'oklch(90% 0.04 145)',
        danger: 'oklch(52% 0.17 25)',
        'danger-soft': 'oklch(89% 0.055 32)',
      },
      fontFamily: {
        display: ['ui-rounded', 'Aptos Display', 'Segoe UI', 'system-ui', 'sans-serif'],
        body: ['Aptos', 'Segoe UI', 'BlinkMacSystemFont', 'sans-serif'],
        mono: ['Cascadia Mono', 'SFMono-Regular', 'Consolas', 'ui-monospace', 'monospace'],
      },
      borderRadius: {
        sm: '4px',
        md: '8px',
        lg: '8px',
        pill: '999px',
      },
    },
  },
  plugins: [
    skeleton({
      themes: { preset: [{ name: 'skeleton', enhancements: true }] },
    }),
  ],
} satisfies Config;
