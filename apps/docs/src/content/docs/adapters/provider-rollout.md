---
title: Provider Rollout
description: AI target adapter rollout plan.
---

## v0

- `mock`
- `generic_chat`
- provider safe-mode stubs for listed AI targets, starting with DeepSeek manual-session handoff.

## v1

- DeepSeek Web
- ChatGPT Web
- Claude Web
- Gemini Web
- Mistral Le Chat
- Perplexity Web
- generic chat
- generic form
- mock

## Rules

- User-owned accounts only.
- Manual login through audited live session.
- Provider-specific rate limits.
- No bundled CAPTCHA solver.
- No credential scraping.
- Adapter canary and rollback before full promotion.
