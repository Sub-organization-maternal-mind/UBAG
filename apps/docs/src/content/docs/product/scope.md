---
title: Product Scope
description: UBAG product surface, audiences, goals, and non-goals.
---

## Vision

UBAG is a single self-hostable gateway that lets any application drive web-based AI and automation targets through stable, versioned APIs with operational guarantees.

## Client surfaces

- Desktop apps: Electron, Tauri, .NET, Swift, Qt, GTK, JavaFX, Python, Flutter.
- Backend apps: Node, Python, Go, Rust, Java, Ruby, PHP, Elixir, .NET.
- Mobile apps: iOS, Android, React Native, Flutter, Capacitor, MAUI.
- Browser extensions: Chrome, Edge, Firefox, Safari.
- CLIs and scripts: Bash, PowerShell, Python, Lua, AppleScript.
- No-code systems: n8n, Activepieces, Make-style adapters.
- Legacy apps: localhost sidecar connector.

## Target surfaces

- AI web targets: DeepSeek, ChatGPT, Claude, Gemini, Mistral Le Chat, Perplexity, and future web chat targets.
- Internal web portals: EMR, RIS/PACS, ERP, ticketing, dashboards.
- Generic browser tasks: form fill, extraction, downloads, multistep workflows.

## First implementation boundary

The docs-first Milestone 0 baseline is complete. The repository now includes the current v0 edge foundation slice: gateway contracts/runtime, worker/mock adapter, TypeScript/Python/Go SDK wave, CLI/sidecar, static dashboard prototype, observability/security contracts, and small-profile scaffolding. Live provider execution, runtime SQLite/localfs gateway persistence, full live admin dashboard, production deployment, and compliance activation remain follow-up or external-activation work.
