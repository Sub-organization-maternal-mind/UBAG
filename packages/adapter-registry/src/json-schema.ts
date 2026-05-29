import type { JsonValue } from './types.ts';

/**
 * Minimal, dependency-free JSON Schema (draft-07 subset) validator.
 *
 * Supported keywords: type, const, enum, required, properties,
 * additionalProperties (boolean), items, minItems, uniqueItems, minLength,
 * pattern, minimum, maximum. This is intentionally small — just enough to
 * validate the adapter manifest and registry index schemas without pulling a
 * runtime dependency into the workspace.
 */
export interface JsonSchema {
  readonly type?: string | readonly string[];
  readonly const?: JsonValue;
  readonly enum?: readonly JsonValue[];
  readonly required?: readonly string[];
  readonly properties?: { readonly [key: string]: JsonSchema };
  readonly additionalProperties?: boolean | JsonSchema;
  readonly items?: JsonSchema;
  readonly minItems?: number;
  readonly uniqueItems?: boolean;
  readonly minLength?: number;
  readonly pattern?: string;
  readonly minimum?: number;
  readonly maximum?: number;
  readonly [key: string]: unknown;
}

function typeOf(value: unknown): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  if (Number.isInteger(value)) return 'integer';
  return typeof value;
}

function matchesType(value: unknown, type: string): boolean {
  const actual = typeOf(value);
  if (type === 'number') {
    return actual === 'number' || actual === 'integer';
  }
  return actual === type;
}

function jsonEqual(a: JsonValue, b: JsonValue): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function validateNode(schema: JsonSchema, value: unknown, path: string, issues: string[]): void {
  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((type) => matchesType(value, type))) {
      issues.push(`${path || '<root>'}: expected type ${types.join('|')}, got ${typeOf(value)}`);
      return;
    }
  }

  if (schema.const !== undefined && !jsonEqual(value as JsonValue, schema.const)) {
    issues.push(`${path}: must equal ${JSON.stringify(schema.const)}`);
  }

  if (schema.enum !== undefined && !schema.enum.some((option) => jsonEqual(value as JsonValue, option))) {
    issues.push(`${path}: must be one of ${schema.enum.map((o) => JSON.stringify(o)).join(', ')}`);
  }

  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength) {
      issues.push(`${path}: string shorter than minLength ${schema.minLength}`);
    }
    if (schema.pattern !== undefined && !new RegExp(schema.pattern).test(value)) {
      issues.push(`${path}: does not match pattern ${schema.pattern}`);
    }
  }

  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) {
      issues.push(`${path}: ${value} < minimum ${schema.minimum}`);
    }
    if (schema.maximum !== undefined && value > schema.maximum) {
      issues.push(`${path}: ${value} > maximum ${schema.maximum}`);
    }
  }

  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems) {
      issues.push(`${path}: array shorter than minItems ${schema.minItems}`);
    }
    if (schema.uniqueItems === true) {
      const seen = new Set<string>();
      for (const item of value) {
        const key = JSON.stringify(item);
        if (seen.has(key)) {
          issues.push(`${path}: array items must be unique`);
          break;
        }
        seen.add(key);
      }
    }
    if (schema.items !== undefined) {
      value.forEach((item, index) => validateNode(schema.items!, item, `${path}[${index}]`, issues));
    }
  }

  if (typeOf(value) === 'object') {
    const record = value as Record<string, unknown>;
    for (const key of schema.required ?? []) {
      if (!(key in record)) {
        issues.push(`${path || '<root>'}: missing required property "${key}"`);
      }
    }
    const properties = schema.properties ?? {};
    for (const [key, child] of Object.entries(properties)) {
      if (key in record) {
        validateNode(child, record[key], path ? `${path}.${key}` : key, issues);
      }
    }
    if (schema.additionalProperties === false) {
      for (const key of Object.keys(record)) {
        if (!(key in properties)) {
          issues.push(`${path || '<root>'}: unexpected property "${key}"`);
        }
      }
    } else if (typeof schema.additionalProperties === 'object') {
      for (const key of Object.keys(record)) {
        if (!(key in properties)) {
          validateNode(schema.additionalProperties, record[key], path ? `${path}.${key}` : key, issues);
        }
      }
    }
  }
}

/** Validate `value` against `schema`. Returns a list of human-readable issues. */
export function validateSchema(schema: JsonSchema, value: unknown): string[] {
  const issues: string[] = [];
  validateNode(schema, value, '', issues);
  return issues;
}
