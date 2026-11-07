'use strict';
const assert = require('node:assert/strict');
const test = require('node:test');
const path = require('node:path');
const lib = require('./lib.js');
const launcher = require('../scripts/run-engram.js');
const project = { project_id: '46716bdb-e96d-42dd-9e37-bb2a6c1190f1', name: 'Logical fixture' };

test('MCP launcher inherits host cwd, never package cwd or PWD', () => {
 assert.equal(launcher.resolveHostRoot({}, '/host/project'), path.resolve('/host/project'));
 assert.equal(launcher.resolveHostRoot({ PWD: '/foreign/project', PLUGIN_ROOT: '/package/root' }, '/host/project'), path.resolve('/host/project'));
 assert.equal(launcher.resolveHostRoot({ CLAUDE_PROJECT_DIR: '/claude/session' }, '/host/project'), path.resolve('/claude/session'));
});

test('missing host hook context fails before binary lookup', async () => {
 await assert.rejects(lib.resolveHookProjectContext(''), /PROJECT_CONTEXT_REQUIRED/);
});

test('project resolution shares the caller deadline with acquisition and never executes after cancellation', async (t) => {
 let now = 1000;
 t.mock.method(Date, 'now', () => now);
 const calls = [];
 const options = {
  timeoutMs: 100,
  resolve: async (supplied) => {
   calls.push({ deadlineAt: supplied.deadlineAt, timeoutMs: supplied.timeoutMs });
   now += 75;
   return { path: 'verified-client', target: {} };
  },
  hash: () => true, roots: () => ({ objects: 'objects' }),
  execFile(_file, _args, supplied, callback) {
   calls.push({ processTimeout: supplied.timeout });
   callback(null, JSON.stringify(project));
  },
 };
 assert.deepEqual(await lib.resolveHookProjectContext('/actual/session/root', options),
  { Project: project.project_id, LogicalProject: project });
 assert.deepEqual(calls, [{ deadlineAt: 1100, timeoutMs: 100 }, { processTimeout: 25 }]);

 const controller = new AbortController();
 controller.abort();
 await assert.rejects(lib.resolveHookProjectContext('/actual/session/root', { ...options, signal: controller.signal }), /PROJECT_RESOLUTION_ABORTED/);
 assert.equal(calls.length, 2, 'already-aborted calls must not acquire or execute');

 await assert.rejects(lib.resolveHookProjectContext('/actual/session/root', {
  ...options,
  resolve: async () => { now += 100; return { path: 'verified-client', target: {} }; },
 }), /PROJECT_RESOLUTION_DEADLINE_EXCEEDED/);
 assert.equal(calls.length, 2, 'acquisition must not restart the process budget');
});

test('client acquisition and read-only project inspection are independent options', async () => {
 const calls = [];
 const options = {
  acquire: false,
  resolve: async supplied => { assert.equal(supplied.acquire, false); return { path: 'verified-client', target: {} }; },
  hash: () => true, roots: () => ({ objects: 'objects' }),
  execFile(_file, args, _options, callback) { calls.push(args); callback(null, JSON.stringify(args.includes('--check') ? { status: 'resolved', binding: { id: project.project_id, match: 'portable_marker' } } : project)); },
 };
 await lib.resolveHookProjectContext('/actual/root', options);
 await lib.resolveHookProjectContext('/actual/root', { ...options, check: true });
 assert.deepEqual(calls, [
  ['project', 'resolve', '--root', '/actual/root', '--json'],
  ['project', 'resolve', '--root', '/actual/root', '--json', '--check'],
 ]);
});

test('inspect-only resolution consumes resolved ProjectInspection rather than ordinary resolver output', async () => {
 let inspection = { status: 'resolved', binding: { id: project.project_id, match: 'portable_marker', project_key: null, identity_status: null } };
 const options = {
  check: true, acquire: false,
  resolve: async () => ({ path: 'verified-client', target: {} }),
  hash: () => true, roots: () => ({ objects: 'objects' }),
  execFile(_file, args, _options, callback) { assert.equal(args.at(-1), '--check'); callback(null, JSON.stringify(inspection)); },
 };
 assert.equal((await lib.resolveHookProjectContext('/actual/root', options)).Project, project.project_id);
 for (const invalid of [
  { status: 'unbound' }, { status: 'conflict' },
  { status: 'migration_required', binding: { id: project.project_id, match: 'portable_marker' } },
  { status: 'resolved', binding: { id: project.project_id, match: 'portable_marker', removed: true } },
  { status: 'resolved', binding: { id: project.project_id, match: 'portable_marker', identity_status: 'inactive' } },
  { project_id: project.project_id },
 ]) {
  inspection = invalid;
  await assert.rejects(lib.resolveHookProjectContext('/actual/root', options), /PROJECT_/);
 }
});
