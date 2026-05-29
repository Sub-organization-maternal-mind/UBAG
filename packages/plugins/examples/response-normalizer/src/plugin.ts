// Guest source for the response-normalizer plugin.
//
// In production this compiles to `build/response_normalizer.wasm`. The mock
// executor imports the exported `plugin` object directly so the host pipeline
// can be tested without a WASM toolchain. Type imports are erased at runtime,
// so there is no runtime dependency on the host package.
import type { GuestContext, GuestModule, PluginJsonValue } from '../../../src/index.ts';

function isObject(value: PluginJsonValue): value is { [key: string]: PluginJsonValue } {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function normalizeText(text: string): string {
  return text
    .replace(/\r\n/g, '\n')
    .replace(/[ \t]+$/gm, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

export const plugin: GuestModule = {
  transform(input: PluginJsonValue, ctx: GuestContext): PluginJsonValue {
    ctx.log('debug', `normalizing response for ${ctx.pluginId}`);

    if (!isObject(input)) {
      return input;
    }

    const output: { [key: string]: PluginJsonValue } = { ...input };
    if (typeof output['text'] === 'string') {
      output['text'] = normalizeText(output['text']);
    }
    if (output['finish_reason'] === undefined) {
      output['finish_reason'] = 'stop';
    }
    output['normalized_at'] = ctx.clock();
    return output;
  },
};

export default plugin;
