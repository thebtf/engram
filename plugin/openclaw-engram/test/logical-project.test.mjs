import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { execFileSync } from 'node:child_process';
import test from 'node:test';
const compiled = process.env.ENGRAM_OPENCLAW_TEST_COMPILED || fileURLToPath(new URL('../dist/', import.meta.url));
const { resolveIdentity } = await import(pathToFileURL(path.join(compiled, 'identity.js')));
const { EngramRestClient, resolveProject } = await import(pathToFileURL(path.join(compiled, 'client.js')));
const executable = process.env.ENGRAM_EXECUTABLE;

test('OpenClaw consumes the real Go UUID across concurrent roots and moves', async (t) => {
 assert.ok(executable, 'Set ENGRAM_EXECUTABLE to the built candidate Go client');
 const base = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-logical-'));
 t.after(() => fs.rmSync(base, { recursive: true, force: true }));
 const roots = ['one/same', 'two/same'].map(relative => path.join(base, relative));
 for (const root of roots) {
  fs.mkdirSync(root, { recursive: true });
  execFileSync('git', ['init', '--quiet'], { cwd: root, stdio: 'pipe' });
  execFileSync(executable, ['project', 'init'], { cwd: root, stdio: 'pipe' });
 }
 const ids = roots.map(root => JSON.parse(execFileSync(executable, ['project', 'resolve', '--root', root, '--json'], { encoding: 'utf8' })).project_id);
 assert.notEqual(ids[0], ids[1]);
 const identities = await Promise.all([resolveIdentity('agent-a', roots[0]), resolveIdentity('agent-b', roots[1]), resolveIdentity('agent-c', roots[0])]);
 assert.deepEqual(identities.map(identity => identity.projectId), [ids[0], ids[1], ids[0]]);
 const moved = path.join(base, 'moved');
 fs.renameSync(roots[0], moved);
 assert.equal((await resolveIdentity('agent-a', moved)).projectId, ids[0]);
 await assert.rejects(resolveIdentity('agent-a'), /PROJECT_CONTEXT_REQUIRED/);
 const client = new EngramRestClient({ url: 'http://127.0.0.1:9', token: '', timeoutMs: 100 });
 const result = await resolveProject(client, 'agent-a', moved);
 assert.equal(result.canonicalProject, ids[0]);
 assert.equal(result.ok, true);
 const absent = await resolveProject(client, 'agent-a');
 assert.equal(absent.ok, false);
 assert.equal(absent.error.code, 'PROJECT_CONTEXT_REQUIRED');
});

test('OpenClaw consumes native historical migration refusal without inventing identity', async t => {
 assert.ok(executable, 'Set ENGRAM_EXECUTABLE to the built candidate Go client');
 const root = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-legacy-'));
 t.after(() => fs.rmSync(root, { recursive: true, force: true }));
 execFileSync('git', ['init', '--quiet', '--template='], { cwd: root, stdio: 'pipe' });
 const marker = path.join(root, '.engram-project');
 const raw = Buffer.from('{"id":"941b49","name":"ai-warden"}\n');
 fs.writeFileSync(marker, raw);
 const env = { ...process.env, ENGRAM_EXECUTABLE: executable, ENGRAM_URL: 'http://127.0.0.1:9', ENGRAM_SERVER_URL: '', ENGRAM_TOKEN: '' };
 await assert.rejects(resolveIdentity('legacy-agent', root, { env }), error => {
  assert.equal(error.message, 'PROJECT_MIGRATION_REQUIRED');
  assert.match(error.guidance, /GET \/api\/projects\/resolve\?legacy_id=/);
  assert.match(error.guidance, /not a mapping/);
  assert.ok(error.guidance.length <= 3072, 'native guidance must fit the existing adapter bound');
  return true;
 });
 const client = { resolveWorkspaceProject: () => resolveIdentity('legacy-agent', root, { env }) };
 const held = await resolveProject(client, 'legacy-agent', root);
 assert.equal(held.ok, false);
 assert.equal(held.error.code, 'PROJECT_MIGRATION_REQUIRED');
 assert.match(held.error.upgradeAction, /legacy_marker/);
 const check = JSON.parse(execFileSync(executable, ['project', 'resolve', '--root', root, '--json', '--check'], { env, encoding: 'utf8' }));
 assert.deepEqual(check, { status: 'migration_required', binding: { id: '941b49', project_key: null, identity_status: null, match: 'legacy_marker' } });
 assert.deepEqual(fs.readFileSync(marker), raw);
 assert.equal(fs.existsSync(path.join(root, '.git', 'engram-project.json')), false);
});
