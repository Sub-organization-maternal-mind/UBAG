export const dashboardData = {
  lastSync: '2026-05-23 08:10 PKT',
  summary: [
    {
      label: 'Gateway',
      value: 'Mock',
      state: 'Not connected',
      source: 'Local seed',
      tone: 'blocked'
    },
    {
      label: 'Queued jobs',
      value: '7',
      state: 'Needs review',
      source: 'Mock queue',
      tone: 'review'
    },
    {
      label: 'Active sessions',
      value: '3',
      state: 'Ready',
      source: 'Mock worker pool',
      tone: 'ready'
    },
    {
      label: 'Adapter drift',
      value: '2',
      state: 'Needs review',
      source: 'Mock canary',
      tone: 'review'
    }
  ],
  operatorItems: [
    {
      title: 'Validate idempotency replay path',
      owner: 'Gateway',
      status: 'Ready',
      nextStep: 'Run local fixture after API wiring'
    },
    {
      title: 'Review manual-login session policy',
      owner: 'Worker',
      status: 'Needs review',
      nextStep: 'Confirm timeout and artifact retention'
    },
    {
      title: 'Publish template variable contract',
      owner: 'Templates',
      status: 'Draft',
      nextStep: 'Lock schema before first live adapter'
    }
  ],
  apps: [
    {
      name: 'Console Smoke App',
      environment: 'edge-local',
      auth: 'App secret configured',
      quota: 'Not enforced',
      status: 'Ready',
      nextStep: 'Use for dry-run jobs'
    },
    {
      name: 'CLI Sandbox',
      environment: 'edge-local',
      auth: 'App secret configured',
      quota: 'Not enforced',
      status: 'Ready',
      nextStep: 'Verify SDK command flow'
    },
    {
      name: 'Workflow Prototype',
      environment: 'edge-local',
      auth: 'Missing secret',
      quota: 'Not enforced',
      status: 'Blocked',
      nextStep: 'Create app secret before queue access'
    }
  ],
  targets: [
    {
      name: 'Mock target',
      adapter: 'ubag_mock_adapter',
      drift: 'Ready',
      login: 'No login required',
      health: 'Ready',
      nextStep: 'Keep as conformance baseline'
    },
    {
      name: 'Generic browser target',
      adapter: 'generic-web',
      drift: 'Needs review',
      login: 'Manual login planned',
      health: 'Draft',
      nextStep: 'Add drift fixture before live use'
    },
    {
      name: 'First real adapter path',
      adapter: 'to be selected',
      drift: 'Draft',
      login: 'User-owned login required',
      health: 'Not connected',
      nextStep: 'Select provider and consent controls'
    }
  ],
  jobs: [
    {
      id: 'job_mock_014',
      app: 'Console Smoke App',
      target: 'Mock target',
      status: 'Ready',
      retry: 'None',
      idempotency: 'idem_edge_14',
      updated: '08:08'
    },
    {
      id: 'job_mock_013',
      app: 'CLI Sandbox',
      target: 'Mock target',
      status: 'Needs review',
      retry: '1 pending',
      idempotency: 'idem_cli_04',
      updated: '08:02'
    },
    {
      id: 'job_mock_012',
      app: 'Workflow Prototype',
      target: 'Generic browser target',
      status: 'Blocked',
      retry: 'Held',
      idempotency: 'idem_flow_02',
      updated: '07:54'
    }
  ],
  sessions: [
    {
      id: 'session_edge_003',
      target: 'Mock target',
      browser: 'Chromium',
      status: 'Ready',
      operator: 'No operator needed',
      artifact: 'JSONL event stream'
    },
    {
      id: 'session_edge_002',
      target: 'Generic browser target',
      browser: 'Chromium',
      status: 'Needs review',
      operator: 'Manual login window planned',
      artifact: 'Screenshot capture draft'
    },
    {
      id: 'session_edge_001',
      target: 'First real adapter path',
      browser: 'Pending',
      status: 'Not connected',
      operator: 'Consent controls required',
      artifact: 'None'
    }
  ],
  templates: [
    {
      name: 'Summarize page',
      mode: 'Safe automation',
      variables: 'url, instruction',
      status: 'Draft',
      nextStep: 'Add output schema'
    },
    {
      name: 'Extract table',
      mode: 'Safe automation',
      variables: 'url, selector',
      status: 'Needs review',
      nextStep: 'Define failure language'
    },
    {
      name: 'Mock completion',
      mode: 'Conformance',
      variables: 'prompt, seed',
      status: 'Ready',
      nextStep: 'Use for fixture parity'
    }
  ],
  runtime: [
    {
      surface: 'Gateway',
      mode: '/v1 health, ready, version, metrics',
      readiness: 'Implemented',
      source: 'Runtime probe and gateway tests',
      nextStep: 'Configure live base URL before connecting dashboard'
    },
    {
      surface: 'Executor and worker',
      mode: 'noop, file-spool, NATS worker consumer',
      readiness: 'Implemented',
      source: 'Gateway and worker suites',
      nextStep: 'Enable file or NATS mode with operator-owned config'
    },
    {
      surface: 'Postgres, MinIO, webhooks',
      mode: 'Opt-in small profile stores and signed outbox',
      readiness: 'Implemented',
      source: 'Deployment and gateway checks',
      nextStep: 'Provide allowlisted callback hosts and secrets'
    },
    {
      surface: 'SSE and WebSocket',
      mode: 'SSE events plus guarded WebSocket baseline',
      readiness: 'Implemented',
      source: 'Gateway route tests',
      nextStep: 'Use richer bidirectional semantics in later dashboard wiring'
    }
  ],
  activation: [
    {
      area: 'Live AI provider adapters',
      state: 'External activation',
      operatorInput: 'User-owned accounts, manual sessions, consent',
      guardrail: 'No credential capture, no CAPTCHA bypass, runtime noVNC only'
    },
    {
      area: 'Workflow/template/cache runtime',
      state: 'Implemented',
      operatorInput: 'Use built-in template and exact cache policies',
      guardrail: 'Tenant/app scope, idempotency, payload safety, cache privacy'
    },
    {
      area: 'gRPC/gRPC-Web',
      state: 'Not yet served',
      operatorInput: 'Transport choice and CORS/origin policy',
      guardrail: 'Must reuse REST auth, idempotency, stable errors, limits'
    },
    {
      area: 'Small-profile smoke',
      state: 'External activation',
      operatorInput: 'Docker Linux engine and non-placeholder env.local',
      guardrail: 'Loopback backing ports and outbound webhook allowlist'
    }
  ],
  activity: [
    {
      time: '08:10',
      label: 'Dashboard booted with local mock data',
      status: 'Ready'
    },
    {
      time: '08:08',
      label: 'Mock target emitted deterministic response',
      status: 'Ready'
    },
    {
      time: '08:02',
      label: 'Retry path marked for operator review',
      status: 'Needs review'
    },
    {
      time: '07:54',
      label: 'Workflow prototype blocked by missing app secret',
      status: 'Blocked'
    }
  ],
  browserSummary: [
    {
      label: 'Browser instances',
      value: '3',
      state: 'Ready',
      source: 'Mock /v1/browser/summary',
      tone: 'ready'
    },
    {
      label: 'Provider contexts',
      value: '5',
      state: 'Ready',
      source: 'Mock /v1/browser/summary',
      tone: 'ready'
    },
    {
      label: 'Channel tabs',
      value: '11',
      state: 'Needs review',
      source: 'Mock /v1/browser/summary',
      tone: 'review'
    },
    {
      label: 'Quarantined tabs',
      value: '1',
      state: 'Blocked',
      source: 'Mock /v1/browser/summary',
      tone: 'blocked'
    }
  ],
  browserInstances: [
    {
      instance: 'browser_inst_01',
      context: 'ctx_openai_primary',
      provider: 'chatgpt_web',
      identity: 'acct-alpha',
      tab: 'tab_openai_a1',
      state: 'ready',
      job: 'job_mock_014',
      storage: 'Snapshot present',
      engine: 'Chromium (CDP)'
    },
    {
      instance: 'browser_inst_01',
      context: 'ctx_openai_primary',
      provider: 'chatgpt_web',
      identity: 'acct-alpha',
      tab: 'tab_openai_a2',
      state: 'busy',
      job: 'job_mock_018',
      storage: 'Snapshot present',
      engine: 'Chromium (CDP)'
    },
    {
      instance: 'browser_inst_01',
      context: 'ctx_claude_primary',
      provider: 'claude_web',
      identity: 'acct-alpha',
      tab: 'tab_claude_b1',
      state: 'warming',
      job: '—',
      storage: 'Snapshot present',
      engine: 'Chromium (CDP)'
    },
    {
      instance: 'browser_inst_02',
      context: 'ctx_gemini_primary',
      provider: 'gemini_web',
      identity: 'acct-beta',
      tab: 'tab_gemini_c1',
      state: 'draining',
      job: '—',
      storage: 'No snapshot yet',
      engine: 'Firefox (BiDi)'
    },
    {
      instance: 'browser_inst_03',
      context: 'ctx_perplexity_primary',
      provider: 'perplexity_web',
      identity: 'acct-beta',
      tab: 'tab_ppx_d1',
      state: 'quarantined',
      job: '—',
      storage: 'Snapshot present',
      engine: 'WebKit (BiDi)'
    }
  ],
  concurrency: [
    {
      provider: 'chatgpt_web',
      identity: 'acct-alpha',
      cap: '4',
      bounds: '1 / 6',
      inflight: '3',
      lastChange: 'Increase · success streak',
      state: 'Ready'
    },
    {
      provider: 'claude_web',
      identity: 'acct-alpha',
      cap: '2',
      bounds: '1 / 5',
      inflight: '1',
      lastChange: 'Hold · warming pool',
      state: 'Needs review'
    },
    {
      provider: 'gemini_web',
      identity: 'acct-beta',
      cap: '2',
      bounds: '1 / 6',
      inflight: '0',
      lastChange: 'Decrease · soft slow-down banner',
      state: 'Needs review'
    },
    {
      provider: 'perplexity_web',
      identity: 'acct-beta',
      cap: '1',
      bounds: '1 / 4',
      inflight: '0',
      lastChange: 'Cut · CAPTCHA signal, cooldown',
      state: 'Blocked'
    }
  ],
  alerts: [
    {
      id: 'alert_captcha_01',
      kind: 'captcha',
      job: 'job_mock_018',
      target: 'chatgpt_web · acct-alpha',
      age: '2m',
      status: 'open',
      detail: 'CAPTCHA wall detected mid-job. A human operator must solve it via noVNC; UBAG never auto-solves.'
    },
    {
      id: 'alert_login_02',
      kind: 'manual_login',
      job: 'job_mock_021',
      target: 'gemini_web · acct-beta',
      age: '6m',
      status: 'acknowledged',
      detail: 'Manual login / 2FA required. Operator completes the user-owned login in the live session.'
    }
  ],
  alertConfig: [
    {
      setting: 'Sink',
      value: 'SMTP email + dashboard queue'
    },
    {
      setting: 'SMTP configured',
      value: 'yes'
    },
    {
      setting: 'Default recipient',
      value: 'mindreader420123@gmail.com'
    },
    {
      setting: 'Resolution',
      value: 'Human-solved (noVNC takeover) — no automated CAPTCHA bypass'
    }
  ],
  stateFixtures: [
    {
      state: 'loading',
      title: 'Loading live gateway state',
      detail: 'Skeleton rows are shown before a configured gateway responds.',
      aria: 'Loading dashboard data from the local gateway.'
    },
    {
      state: 'empty',
      title: 'No jobs match this view',
      detail: 'The empty state keeps table structure understandable when filters return no rows.',
      aria: 'No dashboard records match the current view.'
    },
    {
      state: 'partial',
      title: 'Partial data available',
      detail: 'Gateway health loaded, but optional observability services did not answer yet.',
      aria: 'Dashboard data is partially available.'
    },
    {
      state: 'error',
      title: 'Gateway request failed',
      detail: 'Operators get a named error region and can retry without losing context.',
      aria: 'Dashboard data request failed.'
    },
    {
      state: 'permission-denied',
      title: 'Permission denied',
      detail: 'Role-scoped sections explain the missing permission instead of rendering blank content.',
      aria: 'Dashboard section permission is denied.'
    },
    {
      state: 'stale',
      title: 'Offline or stale',
      detail: 'Last-known data is marked stale when the gateway or browser connection is unavailable.',
      aria: 'Dashboard data is stale or offline.'
    }
  ]
};

export const dashboardTabs = [
  'overview',
  'apps',
  'targets',
  'jobs',
  'sessions',
  'browser',
  'concurrency',
  'alerts',
  'templates',
  'runtime',
  'activation'
];
