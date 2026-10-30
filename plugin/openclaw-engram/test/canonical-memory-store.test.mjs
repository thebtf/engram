import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { pathToFileURL, fileURLToPath } from 'node:url';
import test from 'node:test';

const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const { EngramRestClient } = await import(pathToFileURL(path.join(compiled, 'client.js')));
const { createEngramRememberTool } = await import(pathToFileURL(path.join(compiled, 'tools/engram-remember.js')));
const { createMemoryGetTool } = await import(pathToFileURL(path.join(compiled, 'tools/memory-get.js')));
const { createMemoryMigrateTool } = await import(pathToFileURL(path.join(compiled, 'tools/memory-migrate.js')));
const { createFileWatcherService } = await import(pathToFileURL(path.join(compiled, 'services/file-watcher.js')));
const { syncMemoryFile, loadMarker, MARKER_FILE } = await import(pathToFileURL(path.join(compiled, 'utils/memory-files.js')));
const { buildRememberCommand } = await import(pathToFileURL(path.join(compiled, 'commands/remember.js')));
const imported = await import(pathToFileURL(path.join(compiled, 'index.js')));
const plugin = imported.default.default ?? imported.default;
const project = '11111111-1111-4111-8111-111111111111';
const otherProject = '22222222-2222-4222-8222-222222222222';
const logger = Object.fromEntries(['debug', 'info', 'warn', 'error'].map(level => [level, () => { }]));
const input = { project, title: 'Synthetic title', content: 'Synthetic content', type: 'decision', scope: 'project', tags: ['original'] };
const config = { url: 'http://127.0.0.1:9', token: 'synthetic-fixture-only', project, timeoutMs: 500, autoExtract: false };

function workspace(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-canonical-store-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function fixture(t, reply) {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    const target = new URL(String(url));
    assert.equal(target.origin, config.url, 'Unit fixture must never access a live host');
    assert.equal(target.pathname, '/api/memories', 'No retired import endpoint or fallback');
    assert.equal(options.method, 'POST');
    const body = JSON.parse(options.body);
    requests.push(body);
    const response = reply ? await reply(body, requests.length) : { id: requests.length, project: body.project, content: body.content };
    return response instanceof Response ? response : new Response(JSON.stringify(response), { status: 201 });
  });
  return { client: new EngramRestClient(config), requests };
}

test('canonical memory writer preserves title/type/tags, exact project and supplied attribution', async t => {
  const { client, requests } = fixture(t);
  const results = await client.storeMemories([input], { sourceAgent: 'host-agent', sessionId: 'actual-conversation' });
  assert.equal(results[0].ok, true);
  assert.equal(results[0].memory.id, 1);
  assert.deepEqual(requests, [{
    project, content: 'Synthetic title\n\nSynthetic content', tags: ['original', 'type:decision'],
    privacy_scope: 'project', source_agent: 'host-agent', session_id: 'actual-conversation',
  }]);
  assert.equal('owner_principal' in requests[0], false, 'Owner is derived by the server, never supplied from host context');
});

test('agent privacy is explicit server principal visibility, never workstation-private or implicit shared', async t => {
  const { client, requests } = fixture(t);
  const results = await client.storeMemories([{ ...input, scope: 'agent' }, { ...input, project: otherProject, scope: 'global' }]);
  assert.deepEqual(results.map(result => result.ok), [true, true]);
  assert.equal(requests[0].agent_visibility, 'private');
  assert.equal(requests[0].privacy_scope, 'project');
  assert.equal(requests[1].project, otherProject);
  assert.equal(requests[1].privacy_scope, 'global');
  assert.equal('agent_visibility' in requests[1], false);
  assert.ok(requests.every(body => !('session_id' in body) && !('source_agent' in body)));
});

test('batch outcomes retain confirmed IDs and individual errors without duplicates or invented success counts', async t => {
  const { client, requests } = fixture(t, (body, index) => index === 2
    ? new Response('synthetic validation refusal', { status: 400 })
    : { id: index, project: body.project, content: body.content });
  const results = await client.storeMemories([input, { ...input, content: 'refused' }, { ...input, project: otherProject }]);
  assert.equal(requests.length, 3, 'Exactly one request per input, no retry');
  assert.deepEqual(results.map(result => result.ok), [true, false, true]);
  assert.equal(results[0].memory.id, 1);
  assert.match(results[1].error, /HTTP 400.*synthetic validation refusal/);
  assert.equal(results[1].outcome, 'rejected');
  assert.equal(results[2].memory.id, 3);
  assert.deepEqual(await client.storeMemories([]), []);
  assert.equal(requests.length, 3);
});

test('invalid confirmations and lost responses remain unknown instead of imported or duplicate', async t => {
  const { client, requests } = fixture(t, (_body, index) => {
    if (index === 1) return { imported: 1, skipped_duplicates: 0 };
    throw new Error('synthetic transport lost acknowledgement');
  });
  const results = await client.storeMemories([input, input]);
  assert.equal(requests.length, 2);
  assert.deepEqual(results.map(result => result.ok), [false, false]);
  assert.match(results[0].error, /unknown/);
  assert.ok(results.every(result => result.outcome === 'unknown'));
  assert.match(results[1].error, /lost acknowledgement.*inspect the outcome/);
});

test('writer tools preserve sessionId precedence and the host-provided sessionKey fallback', async t => {
  const { client, requests } = fixture(t);
  for (const [context, expected] of [
    [{ agentId: 'host-agent', sessionId: 'preferred-session', sessionKey: 'fallback-key' }, 'preferred-session'],
    [{ agentId: 'host-agent', sessionKey: 'fallback-key' }, 'fallback-key'],
    [{ agentId: 'host-agent' }, undefined],
  ]) {
    const tool = createEngramRememberTool(context, client, config);
    assert.match(await tool.execute('synthetic-call', { title: 'A', content: 'B', scope: 'agent' }), /^Stored:/);
    const request = requests.at(-1);
    assert.equal(request.source_agent, 'host-agent');
    assert.equal(request.session_id, expected);
    assert.equal(request.agent_visibility, 'private');
    assert.notEqual(request.session_id, project);
  }
});

test('memory_get stores with host session, not project, and reports refused imports honestly', async t => {
  const root = workspace(t);
  fs.writeFileSync(path.join(root, 'note.md'), 'Synthetic file memory');
  const { client, requests } = fixture(t, (body, index) => index === 1
    ? { id: 31, project: body.project, content: body.content }
    : new Response('synthetic refused', { status: 403 }));
  const tool = createMemoryGetTool({ workspaceDir: root, agentId: 'file-agent', sessionId: 'file-conversation', sessionKey: 'file-fallback' }, client, config, { resolvePath: file => path.join(root, file) });
  assert.match(await tool.execute('read-store', { path: 'note.md', store: true }), /Imported into engram/);
  assert.equal(requests[0].session_id, 'file-conversation');
  assert.notEqual(requests[0].session_id, project);
  assert.equal(requests[0].source_agent, 'file-agent');
  const fallback = createMemoryGetTool({ workspaceDir: root, agentId: 'file-agent', sessionKey: 'file-fallback' }, client, config, { resolvePath: file => path.join(root, file) });
  const refused = await fallback.execute('read-refused', { path: 'note.md', store: true });
  assert.equal(requests[1].session_id, 'file-fallback');
  assert.match(refused, /Import failed:/);
  assert.doesNotMatch(refused, /Imported into engram/);
});

test('remember command and registered CLI share canonical writer without inventing a conversation', async t => {
  const { client, requests } = fixture(t);
  const command = buildRememberCommand(client, config);
  assert.match((await command.handler({ args: 'Synthetic command note.' })).text, /^Stored:/);
  let cliRegistrar;
  const api = { config: {}, pluginConfig: config, logger, on() { }, registerTool() { }, registerCommand() { }, registerService() { }, registerCli(callback) { cliRegistrar = callback; } };
  plugin.register(api);
  const actions = new Map();
  const commandNode = name => ({
    command(child) { return commandNode(`${name} ${child}`); },
    description() { return this; }, argument() { return this; }, option() { return this; },
    action(callback) { actions.set(name, callback); return this; },
  });
  cliRegistrar({ program: commandNode('') });
  await actions.get(' memory store')('Synthetic CLI note');
  assert.equal(requests[0].source_agent, 'openclaw-command');
  assert.equal(requests[1].source_agent, 'openclaw-cli');
  assert.ok(requests.every(body => !('session_id' in body)));
});

test('migration checkpoints only confirmed chunks and resumes without replaying successful writes', async t => {
  const root = workspace(t);
  fs.writeFileSync(path.join(root, 'MEMORY.md'), '## First\nSynthetic first memory\n## Second\nSynthetic second memory');
  let refuseSecond = true;
  const { client, requests } = fixture(t, (body, index) => {
    if (body.content.startsWith('Second') && refuseSecond) return new Response('synthetic refusal', { status: 400 });
    return { id: index, project: body.project, content: body.content };
  });
  const tool = createMemoryMigrateTool({ workspaceDir: root, agentId: 'migration-agent', sessionId: 'migration-conversation' }, client, config, { resolvePath: file => path.join(root, file) });
  assert.match(await tool.execute('partial', {}), /Migration incomplete/);
  const partial = await loadMarker(path.join(root, MARKER_FILE));
  assert.equal(partial.files['MEMORY.md'], undefined);
  assert.equal(Object.keys(partial.chunks).length, 1);
  assert.equal(Object.values(partial.writes)[0].state, 'rejected');
  assert.equal(Object.keys(partial.unresolvedAttempts).length, 0);
  refuseSecond = false;
  assert.match(await tool.execute('resume', {}), /Migration complete/);
  assert.equal(requests.length, 3);
  assert.equal(requests.filter(body => body.content.startsWith('First')).length, 1);
  assert.equal(requests[2].session_id, 'migration-conversation');
  assert.equal(requests[2].source_agent, 'migration-agent');
  const complete = await loadMarker(path.join(root, MARKER_FILE));
  assert.equal(typeof complete.files['MEMORY.md'], 'string');
  assert.equal(Object.keys(complete.chunks).length, 2);
  await tool.execute('unchanged', {});
  assert.equal(requests.length, 3);
  await tool.execute('force', { force: true });
  assert.equal(requests.length, 5, 'force explicitly reimports confirmed chunks');
});

test('file checkpoints preserve legacy marker fields while adding confirmed progress', async t => {
  const root = workspace(t);
  const markerPath = path.join(root, MARKER_FILE);
  const old = { lastMigrated: '2026-01-01', files: { 'old.md': 'retained-hash' }, retainedField: 'preserve' };
  fs.writeFileSync(markerPath, JSON.stringify(old));
  const { client, requests } = fixture(t);
  const file = { project, sourcePath: 'new.md', hash: 'new-hash', tag: 'migrated', chunks: [{ title: 'New', content: 'Synthetic new body', type: 'change', sourcePath: 'new.md' }] };
  assert.equal((await syncMemoryFile(client, markerPath, file, {})).stored, 1);
  assert.equal(requests.length, 1);
  const updated = await loadMarker(markerPath);
  assert.equal(updated.files['old.md'], 'retained-hash');
  assert.equal(updated.retainedField, 'preserve');
});

test('watcher never marks a refused file complete and later skips confirmed chunks', async t => {
  const root = workspace(t);
  const file = path.join(root, 'MEMORY.md');
  fs.writeFileSync(file, '## First\nSynthetic first watched memory\n## Second\nSynthetic second watched memory');
  let refuseSecond = true;
  const { client, requests } = fixture(t, (body, index) => body.content.startsWith('Second') && refuseSecond
    ? new Response('synthetic refused', { status: 400 })
    : { id: index, project: body.project, content: body.content });
  const watcher = createFileWatcherService(root, client, config, logger);
  // Exercise the event callback's actual write path without starting a watcher/timer.
  await watcher.syncFile(file);
  assert.equal((await loadMarker(path.join(root, MARKER_FILE))).files['MEMORY.md'], undefined);
  refuseSecond = false;
  await watcher.syncFile(file);
  await watcher.syncFile(file);
  assert.equal(requests.length, 3);
  assert.equal(requests.filter(body => body.content.startsWith('First')).length, 1);
  assert.ok(requests.every(body => body.source_agent === 'file-watcher' && !('session_id' in body)));
});

test('durable pending precedes POST; unknown acknowledgement blocks a repeated watcher event', async t => {
  const root = workspace(t);
  const markerPath = path.join(root, MARKER_FILE);
  const source = path.join(root, 'MEMORY.md');
  fs.writeFileSync(source, '## Synthetic title\nSynthetic body');
  let pendingSnapshot;
  const { client, requests } = fixture(t, async () => {
    pendingSnapshot = fs.readFileSync(markerPath, 'utf8');
    const pending = JSON.parse(pendingSnapshot);
    assert.equal(Object.values(pending.writes)[0].state, 'pending', 'Checkpoint must precede the HTTP side effect');
    throw new Error('synthetic lost acknowledgement');
  });
  const watcher = createFileWatcherService(root, client, config, logger);
  await watcher.syncFile(source);
  const unknown = await loadMarker(markerPath);
  assert.equal(Object.values(unknown.writes)[0].state, 'unknown');
  assert.equal(unknown.files['MEMORY.md'], undefined);
  await watcher.syncFile(source);
  assert.equal(requests.length, 1, 'An event is not permission to repeat an unknown POST');
  // Replay actual pre-POST bytes to model interruption before acknowledgement.
  // A later event must fail closed from durable state, not an in-memory flag.
  fs.writeFileSync(markerPath, pendingSnapshot);
  await watcher.syncFile(source);
  assert.equal(requests.length, 1);
});

test('new project-aware file checkpoints never reuse another project confirmed IDs', async t => {
  const root = workspace(t);
  const markerPath = path.join(root, MARKER_FILE);
  const { client, requests } = fixture(t);
  const file = { project, sourcePath: 'MEMORY.md', hash: 'synthetic-hash', tag: 'migrated', chunks: [{ title: 'Synthetic title', content: 'Synthetic body', type: 'decision', sourcePath: 'MEMORY.md' }] };
  assert.equal((await syncMemoryFile(client, markerPath, file, {})).stored, 1);
  assert.equal((await syncMemoryFile(client, markerPath, { ...file, project: otherProject }, {})).stored, 1);
  assert.equal(requests.length, 2);
  assert.deepEqual(requests.map(body => body.project), [project, otherProject]);
  const marker = await loadMarker(markerPath);
  assert.equal(marker.fileProjects['MEMORY.md'], otherProject);
  assert.equal(Object.keys(marker.chunks).length, 2);
});

test('already-arrived operations for two files queue without dropping either event', async t => {
  const root = workspace(t);
  const markerPath = path.join(root, MARKER_FILE);
  let enterFirst;
  let releaseFirst;
  const entered = new Promise(resolve => { enterFirst = resolve; });
  const release = new Promise(resolve => { releaseFirst = resolve; });
  const { client, requests } = fixture(t, async (body, id) => {
    if (id === 1) {
      enterFirst();
      await release;
    }
    return { id, project: body.project, content: body.content };
  });
  const file = name => ({ project, sourcePath: name, hash: name, tag: 'synced', chunks: [{ title: name, content: `Synthetic ${name}`, type: 'change', sourcePath: name }] });
  const first = syncMemoryFile(client, markerPath, file('first.md'), { sourceAgent: 'file-watcher' });
  await entered;
  const second = syncMemoryFile(client, markerPath, file('second.md'), { sourceAgent: 'file-watcher' });
  releaseFirst();
  const results = await Promise.all([first, second]);
  assert.deepEqual(results.map(result => result.stored), [1, 1]);
  assert.ok(results.every(result => result.error === undefined && result.unresolved === 0));
  assert.equal(requests.length, 2);
  const marker = await loadMarker(markerPath);
  assert.deepEqual(marker.files, { 'first.md': 'first.md', 'second.md': 'second.md' });
  assert.equal(Object.keys(marker.chunks).length, 2);
  assert.deepEqual(marker.writes, {});
  assert.equal(fs.existsSync(markerPath + '.lock'), false, 'No persistent cross-process lock is created');
});

test('simultaneous real watcher callbacks persist progress for both different files', async t => {
  const root = workspace(t);
  const one = path.join(root, 'one.md');
  const two = path.join(root, 'two.md');
  fs.writeFileSync(one, '## One\nSynthetic first watched file');
  fs.writeFileSync(two, '## Two\nSynthetic second watched file');
  const { client, requests } = fixture(t);
  const watcher = createFileWatcherService(root, client, config, logger);
  await Promise.all([watcher.syncFile(one), watcher.syncFile(two)]);
  assert.equal(requests.length, 2);
  const marker = await loadMarker(path.join(root, MARKER_FILE));
  assert.deepEqual(Object.keys(marker.files).sort(), ['one.md', 'two.md']);
  assert.equal(Object.keys(marker.chunks).length, 2);
  assert.deepEqual(marker.writes, {});
});

test('unknown then fully confirmed explicit force retains history and permits new watcher updates', async t => {
  const root = workspace(t);
  fs.writeFileSync(path.join(root, 'MEMORY.md'), '## Synthetic note\nSynthetic unknown migration body');
  let loseAcknowledgement = true;
  const { client, requests } = fixture(t, (body, id) => {
    if (loseAcknowledgement) throw new Error('synthetic lost acknowledgement');
    return { id, project: body.project, content: body.content };
  });
  const tool = createMemoryMigrateTool({ workspaceDir: root, sessionKey: 'migration-fallback' }, client, config, { resolvePath: file => path.join(root, file) });
  assert.match(await tool.execute('unknown', {}), /Migration incomplete/);
  const before = await loadMarker(path.join(root, MARKER_FILE));
  const originalAttempt = Object.values(before.writes)[0].attemptId;
  await tool.execute('ordinary-repeat', {});
  assert.equal(requests.length, 1);
  loseAcknowledgement = false;
  const forced = await tool.execute('deliberate-reimport', { force: true });
  assert.equal(requests.length, 2);
  assert.equal(requests[1].session_id, 'migration-fallback');
  assert.match(forced, /Migration complete with acknowledged duplicate risk/);
  assert.match(forced, /Earlier outcomes remain unknown/);
  const after = await loadMarker(path.join(root, MARKER_FILE));
  assert.equal(after.unresolvedAttempts[originalAttempt].state, 'unknown');
  assert.equal(Object.keys(after.chunks).length, 1);
  const acknowledgement = after.acknowledgedAttempts[originalAttempt];
  assert.equal(acknowledgement.mode, 'force-reimport-with-duplicate-risk');
  assert.deepEqual(acknowledgement.confirmedMemoryIds, [2]);
  assert.equal(acknowledgement.fileHash, after.files['MEMORY.md']);
  assert.equal(acknowledgement.fingerprint, after.unresolvedAttempts[originalAttempt].fingerprint);
  const watcher = createFileWatcherService(root, client, config, logger);
  await watcher.syncFile(path.join(root, 'MEMORY.md'));
  assert.equal(requests.length, 2, 'An unchanged event skips confirmed replacement chunks');
  fs.writeFileSync(path.join(root, 'MEMORY.md'), '## Synthetic note\nSynthetic later watcher update');
  await watcher.syncFile(path.join(root, 'MEMORY.md'));
  assert.equal(requests.length, 3, 'A new update must not remain permanently blocked after acknowledged force success');
  assert.match(requests[2].content, /Synthetic later watcher update/);
  await watcher.syncFile(path.join(root, 'MEMORY.md'));
  assert.equal(requests.length, 3);
  const final = await loadMarker(path.join(root, MARKER_FILE));
  assert.deepEqual(final.unresolvedAttempts[originalAttempt], after.unresolvedAttempts[originalAttempt], 'Unknown history is not rewritten as confirmation');
  assert.deepEqual(final.acknowledgedAttempts[originalAttempt], acknowledgement, 'The duplicate-risk acknowledgement is retained unchanged');
});

test('dry-run does not treat another canonical project file marker as completed', async t => {
  const root = workspace(t);
  fs.writeFileSync(path.join(root, 'MEMORY.md'), '## Synthetic note\nSynthetic project-specific migration');
  const { client, requests } = fixture(t);
  const ctx = { workspaceDir: root };
  const api = { resolvePath: file => path.join(root, file) };
  const first = createMemoryMigrateTool(ctx, client, config, api);
  assert.match(await first.execute('first-project', {}), /Migration complete/);
  const markerPath = path.join(root, MARKER_FILE);
  const before = fs.readFileSync(markerPath, 'utf8');
  const second = createMemoryMigrateTool(ctx, client, { ...config, project: otherProject }, api);
  const preview = await second.execute('other-project-preview', { dryRun: true });
  assert.match(preview, /Dry run: 1 candidate chunk/);
  assert.doesNotMatch(preview, /already migrated/);
  assert.equal(requests.length, 1);
  assert.equal(fs.readFileSync(markerPath, 'utf8'), before, 'Dry-run must not mutate progress');
});

test('short memory with its automatic title is sent only once in canonical content', async t => {
  const { client, requests } = fixture(t);
  const content = 'Synthetic short automatic title';
  const [result] = await client.storeMemories([{ ...input, title: content, content }]);
  assert.equal(result.ok, true);
  assert.equal(requests[0].content, content);
});

test('offline dry-run retains preview without calling an unavailable resolver or backend', async t => {
  const root = workspace(t);
  const body = '## Offline note\nSynthetic offline preview';
  fs.writeFileSync(path.join(root, 'MEMORY.md'), body);
  const markerPath = path.join(root, MARKER_FILE);
  const marker = JSON.stringify({
    lastMigrated: '2026-01-01',
    files: { 'MEMORY.md': createHash('sha256').update(body).digest('hex') },
    fileProjects: { 'MEMORY.md': project },
  });
  fs.writeFileSync(markerPath, marker);
  const calls = [];
  const unavailable = {
    isAvailable() { calls.push('availability'); return false; },
    async resolveWorkspaceProject() { calls.push('resolver'); throw new Error('synthetic resolver unavailable'); },
    async storeMemories() { calls.push('write'); throw new Error('unexpected write'); },
  };
  const tool = createMemoryMigrateTool({ workspaceDir: root }, unavailable, { ...config, project: undefined }, { resolvePath: file => path.join(root, file) });
  const preview = await tool.execute('offline-preview', { dryRun: true });
  assert.match(preview, /Dry run: 1 candidate chunk/);
  assert.match(preview, /Checkpoint project attribution is unverified/);
  assert.match(preview, /Synthetic offline preview/);
  assert.doesNotMatch(preview, /already migrated/);
  assert.deepEqual(calls, [], 'Offline preview must not start native resolution, availability checks or HTTP writes');
  assert.equal(fs.readFileSync(markerPath, 'utf8'), marker);
  assert.deepEqual(fs.readdirSync(root).sort(), [MARKER_FILE, 'MEMORY.md'].sort(), 'No project/bootstrap or checkpoint files are created');
});

test('partial forced reimport does not acknowledge old unknown outcomes or unblock watcher writes', async t => {
  const root = workspace(t);
  const source = path.join(root, 'MEMORY.md');
  fs.writeFileSync(source, '## First\nSynthetic first uncertain body\n## Second\nSynthetic second body');
  let phase = 'unknown';
  const { client, requests } = fixture(t, (body, id) => {
    if (phase === 'unknown') throw new Error('synthetic lost acknowledgement');
    if (body.content.startsWith('Second')) return new Response('synthetic refused second chunk', { status: 400 });
    return { id, project: body.project, content: body.content };
  });
  const tool = createMemoryMigrateTool({ workspaceDir: root }, client, config, { resolvePath: file => path.join(root, file) });
  await tool.execute('unknown-first', {});
  const markerPath = path.join(root, MARKER_FILE);
  const before = await loadMarker(markerPath);
  const original = Object.values(before.writes)[0];
  phase = 'partial';
  const forced = await tool.execute('partial-force', { force: true });
  assert.match(forced, /Migration incomplete/);
  assert.equal(requests.length, 3);
  const after = await loadMarker(markerPath);
  assert.deepEqual(after.unresolvedAttempts[original.attemptId], original);
  assert.deepEqual(after.acknowledgedAttempts ?? {}, {}, 'Only a fully confirmed forced file can acknowledge duplicate risk');
  assert.equal(after.files['MEMORY.md'], undefined);
  assert.equal(Object.keys(after.chunks).length, 1, 'Confirmed partial progress is still retained');
  const watcher = createFileWatcherService(root, client, config, logger);
  await watcher.syncFile(source);
  assert.equal(requests.length, 3, 'A partial force must not silently unblock the earlier unknown');
});

test('unknown A is not acknowledged by forcing different B and cannot replay when the file returns to A', async t => {
  const root = workspace(t);
  const source = path.join(root, 'MEMORY.md');
  const bodyA = '## Synthetic note\nSynthetic uncertain payload A';
  const bodyB = '## Synthetic note\nSynthetic distinct payload B';
  fs.writeFileSync(source, bodyA);
  let loseAcknowledgement = true;
  const { client, requests } = fixture(t, (body, id) => {
    if (loseAcknowledgement) throw new Error('synthetic lost acknowledgement for A');
    return { id, project: body.project, content: body.content };
  });
  const tool = createMemoryMigrateTool({ workspaceDir: root }, client, config, { resolvePath: file => path.join(root, file) });
  await tool.execute('unknown-A', {});
  const markerPath = path.join(root, MARKER_FILE);
  const before = await loadMarker(markerPath);
  const [[fingerprintA, originalA]] = Object.entries(before.writes);
  assert.equal(originalA.state, 'unknown');
  assert.equal(originalA.fingerprint, fingerprintA);
  assert.equal(requests.length, 1);
  fs.writeFileSync(source, bodyB);
  loseAcknowledgement = false;
  const forcedB = await tool.execute('force-B', { force: true });
  assert.equal(requests.length, 2);
  assert.match(requests[1].content, /Synthetic distinct payload B/);
  assert.match(forcedB, /earlier outcomes remain unresolved/);
  const afterB = await loadMarker(markerPath);
  assert.deepEqual(afterB.writes[fingerprintA], originalA, 'Unrelated unknown A must not be swept away by sourcePath');
  assert.equal(afterB.acknowledgedAttempts?.[originalA.attemptId], undefined);
  assert.equal(afterB.chunks[fingerprintA], undefined, 'A has never received a confirmed replacement');
  fs.writeFileSync(source, bodyA);
  const watcher = createFileWatcherService(root, client, config, logger);
  await watcher.syncFile(source);
  await watcher.syncFile(source);
  assert.equal(requests.length, 2, 'Returning to A must not automatically repeat its potentially committed POST');
  assert.equal(requests.filter(body => body.content.includes('Synthetic uncertain payload A')).length, 1);
  const afterA = await loadMarker(markerPath);
  assert.deepEqual(afterA.writes[fingerprintA], originalA);
});
