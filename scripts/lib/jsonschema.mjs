// Minimal, dependency-free JSON Schema validator (subset).
//
// Supports the constructs used by schema/*.schema.json:
//   $ref (local "#/..."), type (string | array), enum, const,
//   required, properties, patternProperties, additionalProperties
//   (boolean | schema), items, minItems, maxItems, uniqueItems,
//   minLength, maxLength, pattern, minimum, maximum,
//   oneOf, anyOf, allOf, not.
//
// It is deliberately small: no network refs, no format, no coercion. When a
// schema grows past this subset, extend this file (and keep it tested).

function typeOf(v) {
  if (v === null) return 'null';
  if (Array.isArray(v)) return 'array';
  if (typeof v === 'number') return Number.isInteger(v) ? 'integer' : 'number';
  return typeof v; // 'string' | 'boolean' | 'object' | 'undefined' | ...
}

function matchesType(v, t) {
  const actual = typeOf(v);
  if (t === 'number') return actual === 'number' || actual === 'integer';
  return actual === t;
}

function resolveRef(root, ref) {
  if (!ref.startsWith('#')) throw new Error(`only local $ref supported: ${ref}`);
  const parts = ref.slice(1).split('/').filter(Boolean)
    .map((p) => p.replace(/~1/g, '/').replace(/~0/g, '~'));
  let cur = root;
  for (const p of parts) {
    if (cur && typeof cur === 'object' && p in cur) cur = cur[p];
    else return undefined;
  }
  return cur;
}

function sortKeys(v) {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === 'object') {
    return Object.fromEntries(Object.keys(v).sort().map((k) => [k, sortKeys(v[k])]));
  }
  return v;
}
function deepEqual(a, b) {
  return JSON.stringify(sortKeys(a)) === JSON.stringify(sortKeys(b));
}

export function validate(data, schema, opts = {}) {
  const root = opts.root || schema;
  const path = opts.path || '$';
  const errors = opts.errors || [];

  if (schema === true || schema === undefined) return errors;
  if (schema === false) { errors.push({ path, message: 'schema is false' }); return errors; }

  if (schema.$ref) {
    const r = resolveRef(root, schema.$ref);
    if (r === undefined) { errors.push({ path, message: `unresolved $ref ${schema.$ref}` }); return errors; }
    return validate(data, r, { root, path, errors });
  }

  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((t) => matchesType(data, t))) {
      errors.push({ path, message: `expected type ${types.join('|')}, got ${typeOf(data)}` });
    }
  }

  if (schema.enum !== undefined && !schema.enum.some((e) => deepEqual(e, data))) {
    errors.push({ path, message: `value ${JSON.stringify(data)} not in enum ${JSON.stringify(schema.enum)}` });
  }

  if (schema.const !== undefined && !deepEqual(schema.const, data)) {
    errors.push({ path, message: `expected const ${JSON.stringify(schema.const)}` });
  }

  if (typeof data === 'string') {
    if (schema.minLength !== undefined && data.length < schema.minLength) errors.push({ path, message: `shorter than ${schema.minLength}` });
    if (schema.maxLength !== undefined && data.length > schema.maxLength) errors.push({ path, message: `longer than ${schema.maxLength}` });
    if (schema.pattern !== undefined && !new RegExp(schema.pattern).test(data)) errors.push({ path, message: `does not match /${schema.pattern}/` });
  }

  if (typeof data === 'number') {
    if (schema.minimum !== undefined && data < schema.minimum) errors.push({ path, message: `less than minimum ${schema.minimum}` });
    if (schema.maximum !== undefined && data > schema.maximum) errors.push({ path, message: `greater than maximum ${schema.maximum}` });
  }

  if (Array.isArray(data)) {
    if (schema.minItems !== undefined && data.length < schema.minItems) errors.push({ path, message: `fewer than ${schema.minItems} items` });
    if (schema.maxItems !== undefined && data.length > schema.maxItems) errors.push({ path, message: `more than ${schema.maxItems} items` });
    if (schema.uniqueItems) {
      for (let i = 0; i < data.length; i++) {
        for (let j = i + 1; j < data.length; j++) {
          if (deepEqual(data[i], data[j])) errors.push({ path: `${path}[${j}]`, message: 'duplicate item' });
        }
      }
    }
    if (schema.items) data.forEach((it, i) => validate(it, schema.items, { root, path: `${path}[${i}]`, errors }));
  }

  if (data && typeof data === 'object' && !Array.isArray(data)) {
    for (const req of schema.required || []) {
      if (!(req in data)) errors.push({ path, message: `missing required "${req}"` });
    }
    const props = schema.properties || {};
    const pats = Object.entries(schema.patternProperties || {});
    for (const [k, v] of Object.entries(data)) {
      if (props[k]) { validate(v, props[k], { root, path: `${path}.${k}`, errors }); continue; }
      let matched = false;
      for (const [p, ps] of pats) {
        if (new RegExp(p).test(k)) { matched = true; validate(v, ps, { root, path: `${path}.${k}`, errors }); }
      }
      if (matched) continue;
      if (schema.additionalProperties === false) errors.push({ path: `${path}.${k}`, message: 'additional property not allowed' });
      else if (schema.additionalProperties && schema.additionalProperties !== true) {
        validate(v, schema.additionalProperties, { root, path: `${path}.${k}`, errors });
      }
    }
  }

  if (schema.oneOf) {
    const matched = schema.oneOf.filter((s) => validate(data, s, { root, path, errors: [] }).length === 0);
    if (matched.length !== 1) errors.push({ path, message: `must match exactly one of oneOf (matched ${matched.length})` });
  }
  if (schema.anyOf) {
    const ok = schema.anyOf.some((s) => validate(data, s, { root, path, errors: [] }).length === 0);
    if (!ok) errors.push({ path, message: 'must match at least one of anyOf' });
  }
  if (schema.allOf) for (const s of schema.allOf) validate(data, s, { root, path, errors });
  if (schema.not) {
    if (validate(data, schema.not, { root, path, errors: [] }).length === 0) errors.push({ path, message: 'must not match "not"' });
  }

  return errors;
}
