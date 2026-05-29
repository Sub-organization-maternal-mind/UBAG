/**
 * Error taxonomy for the UBAG plugin host.
 *
 * Every error carries a stable `code` so the gateway can translate failures
 * into structured job telemetry without string matching.
 */
export type PluginErrorCode =
  | 'manifest_invalid'
  | 'permission_denied'
  | 'capability_unsupported'
  | 'binding_missing'
  | 'execution_failed'
  | 'timeout'
  | 'plugin_not_found';

export class PluginError extends Error {
  readonly code: PluginErrorCode;
  readonly pluginId: string | undefined;

  constructor(code: PluginErrorCode, message: string, pluginId?: string) {
    super(message);
    this.name = 'PluginError';
    this.code = code;
    this.pluginId = pluginId;
  }
}

export class ManifestValidationError extends PluginError {
  readonly issues: readonly string[];

  constructor(issues: readonly string[], pluginId?: string) {
    super('manifest_invalid', `plugin manifest invalid: ${issues.join('; ')}`, pluginId);
    this.name = 'ManifestValidationError';
    this.issues = issues;
  }
}

export class PermissionDeniedError extends PluginError {
  constructor(message: string, pluginId?: string) {
    super('permission_denied', message, pluginId);
    this.name = 'PermissionDeniedError';
  }
}

export class CapabilityUnsupportedError extends PluginError {
  constructor(message: string, pluginId?: string) {
    super('capability_unsupported', message, pluginId);
    this.name = 'CapabilityUnsupportedError';
  }
}

export class PluginExecutionError extends PluginError {
  constructor(message: string, pluginId?: string) {
    super('execution_failed', message, pluginId);
    this.name = 'PluginExecutionError';
  }
}

export class PluginTimeoutError extends PluginError {
  constructor(message: string, pluginId?: string) {
    super('timeout', message, pluginId);
    this.name = 'PluginTimeoutError';
  }
}

export class PluginNotFoundError extends PluginError {
  constructor(message: string, pluginId?: string) {
    super('plugin_not_found', message, pluginId);
    this.name = 'PluginNotFoundError';
  }
}
