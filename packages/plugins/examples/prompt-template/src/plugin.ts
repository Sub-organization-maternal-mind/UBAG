// Guest source for the prompt-template plugin.
//
// Implements two extension points:
//  - transform.prompt: renders {{key}} placeholders from a `variables` map.
//  - hook.job.pre: rejects jobs whose rendered prompt is empty.
import type {
  GuestContext,
  GuestModule,
  HookResult,
  PluginJsonValue,
} from '../../../src/index.ts';

function isObject(value: PluginJsonValue): value is { [key: string]: PluginJsonValue } {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function renderTemplate(template: string, variables: { [key: string]: PluginJsonValue }): string {
  return template.replace(/\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}/g, (match, key: string) => {
    const value = variables[key];
    if (value === undefined || value === null) {
      return '';
    }
    return typeof value === 'string' ? value : JSON.stringify(value);
  });
}

export const plugin: GuestModule = {
  transform(input: PluginJsonValue, ctx: GuestContext): PluginJsonValue {
    if (!isObject(input)) {
      return input;
    }
    const template = input['template'];
    if (typeof template !== 'string') {
      return input;
    }
    const rawVariables = input['variables'];
    const variables = isObject(rawVariables) ? rawVariables : {};
    const rendered = renderTemplate(template, variables);
    ctx.log('debug', `rendered prompt template (${rendered.length} chars)`);

    const output: { [key: string]: PluginJsonValue } = { ...input };
    output['prompt'] = rendered;
    return output;
  },

  hook(event: string, payload: PluginJsonValue, ctx: GuestContext): HookResult {
    if (event !== 'job.pre') {
      return { action: 'continue', payload };
    }
    const prompt = isObject(payload) ? payload['prompt'] : undefined;
    if (typeof prompt !== 'string' || prompt.trim().length === 0) {
      ctx.log('warn', 'rejecting job with empty prompt');
      return { action: 'reject', reason: 'prompt is empty' };
    }
    return { action: 'continue', payload };
  },
};

export default plugin;
