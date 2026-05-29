# UBAG Design System

This project is locked to the Hallmark NAJM demo/theme as the default visual direction.

Reference:

- Live demo: `https://www.usehallmark.com/examples/najm/`
- Local Hallmark example: `D:\Projects\UBAG\.codex\skills\hallmark\site\examples\najm`
- Source tokens: `D:\Projects\UBAG\.codex\skills\hallmark\site\examples\najm\tokens.css`
- Source styles: `D:\Projects\UBAG\.codex\skills\hallmark\site\examples\najm\styles.css`

## Design DNA

- Genre: editorial ecommerce with fashion-drop energy.
- Macrostructure: Marquee Hero.
- Vibe: hyped Moroccan drop, editorial fashion, sun-warm earth.
- Theme route: Hallmark custom theme, light / geometric-sans / warm.
- Visual voice: tactile, confident, patterned, sharp, sun-warmed, retail-ready.
- Surface: warm cream paper, ink text, terracotta primary accent, saffron and marine accents used sparingly.
- Typography: geometric display face for headings and brand moments; neutral sans for body/UI; monospace only for operational metadata or compact labels.
- Motion: restrained but alive; marquee, cart feedback, hover lift, and short easing. Respect reduced-motion.
- Pattern language: zellige/tile references, textile gradients, editorial crop compositions, strong merchandising blocks.

## Core Tokens

Use named tokens in implementation. Do not inline arbitrary color or font values in components when a token can be used.

```css
:root {
  --color-paper: oklch(96.5% 0.012 75);
  --color-paper-soft: oklch(99% 0.006 75);
  --color-paper-warm: oklch(93% 0.020 70);
  --color-ink: oklch(20% 0.022 55);
  --color-ink-soft: oklch(38% 0.018 55);
  --color-ink-mute: oklch(56% 0.012 60);
  --color-rule: oklch(86% 0.014 70);
  --color-rule-soft: oklch(91% 0.010 70);
  --color-accent: oklch(58% 0.18 35);
  --color-accent-deep: oklch(48% 0.20 32);
  --color-accent-soft: oklch(82% 0.08 45);
  --color-saffron: oklch(78% 0.16 78);
  --color-marine: oklch(34% 0.09 240);
  --color-focus-ring: oklch(48% 0.20 32);

  --font-display: "Bricolage Grotesque", -apple-system, system-ui, sans-serif;
  --font-body: "Inter", -apple-system, BlinkMacSystemFont, sans-serif;
  --font-mono: "JetBrains Mono", ui-monospace, monospace;

  --space-1: 0.25rem;
  --space-2: 0.5rem;
  --space-3: 0.75rem;
  --space-4: 1rem;
  --space-5: 1.5rem;
  --space-6: 2rem;
  --space-7: 3rem;
  --space-8: 4rem;
  --space-9: 6rem;
  --space-10: 8rem;
  --space-11: 12rem;

  --radius-sm: 4px;
  --radius-md: 10px;
  --radius-lg: 18px;
  --radius-pill: 999px;
}
```

## Implementation Rules

- Load Hallmark before frontend/UI work and follow its default, audit, redesign, study, or component-scope flow as appropriate.
- Reuse the NAJM token family above unless existing app framework tokens must be mapped into it.
- Keep headings editorial and high-contrast; keep operational UI smaller, denser, and easy to scan.
- Prefer real product/content imagery or intentionally built pattern systems over generic gradients.
- Use warm cream as the main page surface, terracotta as the primary action/brand accent, and saffron/marine as limited supporting accents.
- Keep cards at 8px radius or less unless the NAJM source pattern requires a larger surface radius for an intentional retail block.
- Every interactive component must include default, hover, focus-visible, active, disabled, loading, error, and success states when component-scope applies.
- Verify responsive behavior at 320, 375, 414, and 768 px before final handoff when a runnable frontend exists.
- Use `overflow-x: clip` on `html` and `body` for NAJM-style marquee/pattern systems so animated tracks never create page-level horizontal scroll.
- Do not fabricate statistics, testimonials, customer logos, inventory counts, ratings, or claims.
