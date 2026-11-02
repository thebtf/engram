import assert from 'node:assert/strict';
import fs from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED;
const host = process.env.ENGRAM_OPENCLAW_HOST_FIXTURE;
assert.equal(JSON.parse(fs.readFileSync(path.join(host, 'package.json'), 'utf8')).version, '2026.10.7');
const target = new URL(input.url);
assert.equal(target.protocol, 'http:');
assert.equal(target.hostname, '127.0.0.1');
assert.notEqual(target.port, '');
console.info(JSON.stringify({ compiled, modules: Object.fromEntries(['client.js', 'index.js', 'tools/memory-get.js'].map(file => [file, createHash('sha256').update(fs.readFileSync(path.join(compiled, file))).digest('hex')])) }));
const { EngramRestClient } = await import(pathToFileURL(path.join(compiled, 'client.js')));
const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
const plugin = imported.default.default ?? imported.default;
const config = { url: input.url, token: input.token, project: input.project, timeoutMs: 5000, autoExtract: false };
const client = new EngramRestClient(config);
const networkFetch = globalThis.fetch;
const requests = [];
globalThis.fetch = async (url, options) => {
  assert.equal(new URL(String(url)).origin, target.origin);
  const response = await networkFetch(url, { ...options, redirect: 'error' });
  const body = options?.body && JSON.parse(options.body);
  const text = await response.clone().text();
  const receipt = response.headers.get('content-type')?.includes('application/json') ? JSON.parse(text) : text;
  requests.push({ url: new URL(String(url)), body, status: response.status, receipt });
  // Synthetic content only: keep actual request/receipt bytes when a confirmation fails.
  console.info(JSON.stringify({ path: new URL(String(url)).pathname, body, status: response.status, receipt }));
  return response;
};

// Exercise the plugin's writer callbacks against Go without loading a gateway.
// Official host registration is covered by registered-memory-store.fixture.mjs.
const logger = Object.fromEntries(['info', 'warn', 'error', 'debug'].map(level => [level, () => { }]));
let registrar;
const factories = [];
plugin.register({ config: {}, pluginConfig: config, logger, on() { }, registerTool(factory) { factories.push(factory); }, registerCommand() { }, registerService() { }, registerCli(callback) { registrar = callback; }, resolvePath(file) { return path.join(process.env.HOME, file); } });
const tools = factories.flatMap(factory => factory({ workspaceDir: process.env.HOME, agentId: 'unicode-fixture' }));
const fileTool = tools.find(tool => tool.name === 'memory_get');
assert.ok(fileTool);
const actions = new Map();
const command = name => ({ command(child) { return command(`${name} ${child}`); }, description() { return this; }, argument() { return this; }, option() { return this; }, action(callback) { actions.set(name, callback); return this; } });
registrar({ program: command('') });

const memory = { project: input.project, title: 'boundary', content: 'Synthetic writer boundary', type: 'change', scope: 'project' };
test('real Go JSON decoder replaces an isolated surrogate, not a complete pair', async () => {
  for (const [content, expected] of [['Synthetic raw \ud83d', 'Synthetic raw \ufffd'], ['Synthetic raw 😀', 'Synthetic raw 😀']]) {
    const response = await fetch(target.origin + '/api/memories', { method: 'POST', headers: { Authorization: 'Bearer ' + input.token, 'Content-Type': 'application/json' }, body: JSON.stringify({ project: input.project, content }) });
    assert.equal(response.status, 201);
    assert.equal((await response.json()).content, expected);
  }
});
test('uppercase existing UUID store confirms canonical server identity', async () => {
  const [result] = await client.storeMemories([{ ...memory, project: input.project.toUpperCase() }]);
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(result.memory.project, input.project);
  assert.equal(requests.at(-1).body.project, input.project);
});
test('uppercase existing UUID transition dispatch and receipt share canonical identity', async () => {
  const [stored] = await client.storeMemories([memory]);
  assert.equal(stored.ok, true);
  const result = await client.transitionMemory(stored.memory.id, 'suppress', input.project.toUpperCase());
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(requests.at(-1).url.searchParams.get('project'), input.project);
});
for (const [name, text] of [['content900', 'c'.repeat(899) + '😀tail'], ['title77', 't'.repeat(76) + '😀' + 'c'.repeat(830)]]) {
  test(`registered CLI ${name} receives exact real Go confirmation`, async t => {
    const errors = [], successes = [];
    t.mock.method(console, 'error', message => errors.push(String(message)));
    t.mock.method(console, 'log', message => successes.push(String(message)));
    await actions.get(' memory store')(text);
    assert.deepEqual(errors, []);
    assert.ok(successes.some(message => message.startsWith('Stored:')));
    const sent = requests.at(-1).body.content;
    assert.equal(sent, Buffer.from(sent).toString('utf8'));
  });
}
test('registered local-file 4000 cut confirms exactly and preserves source bytes', async () => {
  const content = 'f'.repeat(3999) + '😀tail';
  const file = path.join(process.env.HOME, 'boundary.md');
  fs.writeFileSync(file, content);
  const result = await fileTool.execute('unicode-file', { path: 'boundary.md', store: true });
  assert.match(result, /Imported into engram/);
  assert.equal(requests.at(-1).body.content, 'boundary\n\n' + 'f'.repeat(3999));
  assert.equal(fs.readFileSync(file, 'utf8'), content);
});
