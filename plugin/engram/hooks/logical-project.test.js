'use strict';
const assert = require('node:assert/strict');
const test = require('node:test');
const path = require('node:path');
const lib = require('./lib.js');
const launcher = require('../scripts/run-engram.js');
const project = { project_id: '46716bdb-e96d-42dd-9e37-bb2a6c1190f1', name: 'Logical fixture' };

test('hook consumes one Go result at the actual call root without registration', async () => {
 const calls = [];
 const context = await lib.resolveHookProjectContext('/actual/session/root', {
  resolve: async () => ({ path: 'verified-client', target: {} }),
  hash: () => true, roots: () => ({ objects: 'objects' }),
  execFile(file, args, options, callback) {
   calls.push({ file, args });
   callback(null, JSON.stringify(project), '');
  },
 });
 assert.deepEqual(context, { Project: project.project_id, LogicalProject: project });
 assert.deepEqual(calls, [{ file: 'verified-client', args: ['project', 'resolve', '--root', '/actual/session/root', '--json'] }]);
});

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
