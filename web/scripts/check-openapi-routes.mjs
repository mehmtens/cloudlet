import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';

const sourcePath = fileURLToPath(new URL('../../internal/httpapi/httpapi.go', import.meta.url));
const specPath = fileURLToPath(new URL('../../openapi.yaml', import.meta.url));
const [source, spec] = await Promise.all([
  readFile(sourcePath, 'utf8'),
  readFile(specPath, 'utf8'),
]);

const sourceOperations = new Set(
  [...source.matchAll(/mux\.Handle(?:Func)?\("(GET|POST|PUT|PATCH|DELETE) ([^"]+)"/g)]
    .map(([, method, path]) => `${method} ${path}`),
);

const specOperations = new Set();
if (spec.trimStart().startsWith('{')) {
  const document = JSON.parse(spec);
  for (const [path, pathItem] of Object.entries(document.paths || {})) {
    for (const method of ['get', 'post', 'put', 'patch', 'delete']) {
      if (pathItem[method]) specOperations.add(`${method.toUpperCase()} ${path}`);
    }
  }
} else {
  let currentPath = '';
  for (const line of spec.split(/\r?\n/)) {
    const pathMatch = line.match(/^  (\/[^:]+):\s*$/);
    if (pathMatch) {
      currentPath = pathMatch[1];
      continue;
    }
    const methodMatch = line.match(/^    (get|post|put|patch|delete):\s*$/);
    if (currentPath && methodMatch) specOperations.add(`${methodMatch[1].toUpperCase()} ${currentPath}`);
  }
}

const missing = [...sourceOperations].filter(operation => !specOperations.has(operation)).sort();
const stale = [...specOperations].filter(operation => !sourceOperations.has(operation)).sort();
if (missing.length || stale.length) {
  if (missing.length) console.error(`Missing OpenAPI operations:\n${missing.join('\n')}`);
  if (stale.length) console.error(`Stale OpenAPI operations:\n${stale.join('\n')}`);
  process.exit(1);
}

console.log(`OpenAPI route parity verified: ${sourceOperations.size} operations across ${new Set([...sourceOperations].map(operation => operation.slice(operation.indexOf(' ') + 1))).size} paths.`);
