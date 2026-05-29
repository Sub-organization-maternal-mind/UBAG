import test from 'node:test';
import assert from 'node:assert/strict';

import { validateSchema } from '../src/index.ts';

const schema = {
  type: 'object',
  additionalProperties: false,
  required: ['id', 'count'],
  properties: {
    id: { type: 'string', pattern: '^[a-z]+$', minLength: 2 },
    count: { type: 'integer', minimum: 0, maximum: 10 },
    kind: { type: 'string', enum: ['a', 'b'] },
    tags: { type: 'array', minItems: 1, uniqueItems: true, items: { type: 'string' } },
  },
};

test('valid object produces no issues', () => {
  assert.deepEqual(
    validateSchema(schema, { id: 'abc', count: 3, kind: 'a', tags: ['x', 'y'] }),
    [],
  );
});

test('reports missing required, bad type, pattern, range, and enum', () => {
  const issues = validateSchema(schema, { count: 99, kind: 'c', tags: [] });
  assert.ok(issues.some((i) => i.includes('missing required property "id"')));
  assert.ok(issues.some((i) => i.includes('maximum 10')));
  assert.ok(issues.some((i) => i.includes('must be one of')));
  assert.ok(issues.some((i) => i.includes('minItems')));
});

test('rejects unexpected properties when additionalProperties is false', () => {
  const issues = validateSchema(schema, { id: 'abc', count: 1, surprise: true });
  assert.ok(issues.some((i) => i.includes('unexpected property "surprise"')));
});

test('detects non-unique array items', () => {
  const issues = validateSchema(schema, { id: 'abc', count: 1, tags: ['x', 'x'] });
  assert.ok(issues.some((i) => i.includes('must be unique')));
});

test('const keyword is enforced', () => {
  const constSchema = { type: 'object', properties: { v: { const: 'ubag.adapter.v0' } } };
  assert.deepEqual(validateSchema(constSchema, { v: 'ubag.adapter.v0' }), []);
  assert.ok(validateSchema(constSchema, { v: 'other' }).length > 0);
});
