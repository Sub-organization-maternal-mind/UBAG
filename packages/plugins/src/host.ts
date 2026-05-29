import {
  CapabilityUnsupportedError,
  PluginExecutionError,
  PluginNotFoundError,
  PluginTimeoutError,
} from './errors.ts';
import type { PluginCapability, PluginJsonValue, PluginManifest } from './manifest.ts';
import { buildGuestContext, type GuestContext, type HostBindings } from './permissions.ts';

export type HookEvent = 'job.pre' | 'job.post';
export type TransformTarget = 'prompt' | 'response';

export interface HookResult {
  /** `continue` proceeds; `reject` aborts the job pipeline with `reason`. */
  readonly action: 'continue' | 'reject';
  readonly payload?: PluginJsonValue;
  readonly reason?: string;
}

/**
 * In-process guest contract.
 *
 * A production WASM build exports `transform` / `hook` functions through the
 * component model. The mock executor binds these symbols to plain functions so
 * the host pipeline can be exercised deterministically without a runtime.
 */
export interface GuestModule {
  transform?(input: PluginJsonValue, ctx: GuestContext): PluginJsonValue | Promise<PluginJsonValue>;
  hook?(
    event: HookEvent,
    payload: PluginJsonValue,
    ctx: GuestContext,
  ): HookResult | Promise<HookResult>;
}

/** A guest module that has been instantiated and is ready to invoke. */
export interface LoadedGuest {
  supports(role: 'transform' | 'hook'): boolean;
  callTransform(input: PluginJsonValue, ctx: GuestContext): Promise<PluginJsonValue>;
  callHook(event: HookEvent, payload: PluginJsonValue, ctx: GuestContext): Promise<HookResult>;
  dispose?(): void | Promise<void>;
}

/**
 * Pluggable backend that turns a manifest + bindings into a {@link LoadedGuest}.
 *
 * Production implementations: `WazeroExecutor` (Go, embeds the .wasm via
 * wazero) and `WasmerExecutor` (TS, `@wasmer/wasi`). Both instantiate the real
 * module referenced by `manifest.entrypoint.module`.
 */
export interface WasmExecutor {
  load(manifest: PluginManifest): LoadedGuest | Promise<LoadedGuest>;
}

export type GuestModuleResolver = (manifest: PluginManifest) => GuestModule | undefined;

/**
 * Deterministic in-memory executor used for tests and offline development.
 *
 * It does not parse `.wasm`; instead it resolves a JS {@link GuestModule} that
 * stands in for the compiled artifact. It still enforces the manifest execution
 * budget via the injected clock so timeout handling is exercised.
 */
export class MockWasmExecutor implements WasmExecutor {
  readonly #resolve: GuestModuleResolver;
  readonly #now: () => number;

  constructor(resolver: GuestModuleResolver, now: () => number = () => Date.now()) {
    this.#resolve = resolver;
    this.#now = now;
  }

  load(manifest: PluginManifest): LoadedGuest {
    const guestModule = this.#resolve(manifest);
    if (guestModule === undefined) {
      throw new PluginExecutionError(
        `mock executor has no module registered for plugin "${manifest.id}" (${manifest.entrypoint.module})`,
        manifest.id,
      );
    }
    const now = this.#now;
    const budgetMs = manifest.permissions.max_execution_ms;

    const enforceBudget = async <T>(
      role: string,
      run: () => T | Promise<T>,
    ): Promise<T> => {
      const startedAt = now();
      const result = await run();
      const elapsed = now() - startedAt;
      if (elapsed > budgetMs) {
        throw new PluginTimeoutError(
          `plugin "${manifest.id}" ${role} exceeded ${budgetMs}ms budget (took ${elapsed}ms)`,
          manifest.id,
        );
      }
      return result;
    };

    return {
      supports(role: 'transform' | 'hook'): boolean {
        return typeof guestModule[role] === 'function';
      },
      async callTransform(input: PluginJsonValue, ctx: GuestContext): Promise<PluginJsonValue> {
        if (typeof guestModule.transform !== 'function') {
          throw new CapabilityUnsupportedError(
            `plugin "${manifest.id}" does not export transform`,
            manifest.id,
          );
        }
        return enforceBudget('transform', () => guestModule.transform!(input, ctx));
      },
      async callHook(
        event: HookEvent,
        payload: PluginJsonValue,
        ctx: GuestContext,
      ): Promise<HookResult> {
        if (typeof guestModule.hook !== 'function') {
          throw new CapabilityUnsupportedError(
            `plugin "${manifest.id}" does not export hook`,
            manifest.id,
          );
        }
        return enforceBudget('hook', () => guestModule.hook!(event, payload, ctx));
      },
    };
  }
}

interface RegisteredPlugin {
  readonly manifest: PluginManifest;
  readonly guest: LoadedGuest;
}

export interface PluginHostOptions {
  readonly executor: WasmExecutor;
  readonly bindings?: HostBindings;
  readonly now?: () => number;
}

const TRANSFORM_CAPABILITY: Record<TransformTarget, PluginCapability> = {
  prompt: 'transform.prompt',
  response: 'transform.response',
};

const HOOK_CAPABILITY: Record<HookEvent, PluginCapability> = {
  'job.pre': 'hook.job.pre',
  'job.post': 'hook.job.post',
};

/**
 * Orchestrates registered plugins across UBAG's two extension surfaces:
 * job pre/post hooks and prompt/response transforms.
 */
export class PluginHost {
  readonly #executor: WasmExecutor;
  readonly #bindings: HostBindings;
  readonly #now: () => number;
  readonly #plugins: RegisteredPlugin[] = [];
  readonly #ids = new Set<string>();

  constructor(options: PluginHostOptions) {
    this.#executor = options.executor;
    this.#bindings = options.bindings ?? {};
    this.#now = options.now ?? (() => Date.now());
  }

  async register(manifest: PluginManifest): Promise<void> {
    if (this.#ids.has(manifest.id)) {
      throw new PluginExecutionError(`plugin "${manifest.id}" is already registered`, manifest.id);
    }
    const guest = await this.#executor.load(manifest);

    const needsTransform = manifest.capabilities.some((cap) => cap.startsWith('transform.'));
    const needsHook = manifest.capabilities.some((cap) => cap.startsWith('hook.'));
    if (needsTransform && !guest.supports('transform')) {
      throw new CapabilityUnsupportedError(
        `plugin "${manifest.id}" declares a transform capability but exports no transform`,
        manifest.id,
      );
    }
    if (needsHook && !guest.supports('hook')) {
      throw new CapabilityUnsupportedError(
        `plugin "${manifest.id}" declares a hook capability but exports no hook`,
        manifest.id,
      );
    }

    this.#plugins.push({ manifest, guest });
    this.#ids.add(manifest.id);
  }

  listPlugins(): readonly PluginManifest[] {
    return this.#plugins.map((entry) => entry.manifest);
  }

  has(pluginId: string): boolean {
    return this.#ids.has(pluginId);
  }

  #context(manifest: PluginManifest, capability: PluginCapability): GuestContext {
    return buildGuestContext(manifest, this.#bindings, { capability, now: this.#now });
  }

  /**
   * Run every plugin that declares `transform.<target>` in registration order,
   * threading each plugin's output into the next. Used for prompt rewriting and
   * response normalization.
   */
  async transform(target: TransformTarget, value: PluginJsonValue): Promise<PluginJsonValue> {
    const capability = TRANSFORM_CAPABILITY[target];
    let current = value;
    for (const { manifest, guest } of this.#plugins) {
      if (!manifest.capabilities.includes(capability)) {
        continue;
      }
      current = await guest.callTransform(current, this.#context(manifest, capability));
    }
    return current;
  }

  /**
   * Run job lifecycle hooks. The first plugin to return `reject` short-circuits
   * the pipeline so the gateway can fail the job before/after execution.
   */
  async runHooks(event: HookEvent, payload: PluginJsonValue): Promise<HookResult> {
    const capability = HOOK_CAPABILITY[event];
    let current = payload;
    for (const { manifest, guest } of this.#plugins) {
      if (!manifest.capabilities.includes(capability)) {
        continue;
      }
      const result = await guest.callHook(event, current, this.#context(manifest, capability));
      if (result.action === 'reject') {
        return result;
      }
      if (result.payload !== undefined) {
        current = result.payload;
      }
    }
    return { action: 'continue', payload: current };
  }

  async invokeTransform(pluginId: string, value: PluginJsonValue): Promise<PluginJsonValue> {
    const entry = this.#plugins.find((plugin) => plugin.manifest.id === pluginId);
    if (entry === undefined) {
      throw new PluginNotFoundError(`plugin "${pluginId}" is not registered`, pluginId);
    }
    const capability = entry.manifest.capabilities.find((cap) => cap.startsWith('transform.'));
    if (capability === undefined) {
      throw new CapabilityUnsupportedError(
        `plugin "${pluginId}" has no transform capability`,
        pluginId,
      );
    }
    return entry.guest.callTransform(value, this.#context(entry.manifest, capability));
  }
}
