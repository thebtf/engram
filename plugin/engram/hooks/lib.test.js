const assert = require('node:assert/strict');
const test = require('node:test');

const lib = require('./lib');

function cleanup(sessionID) {
  lib.clearSessionSignals(sessionID);
}

test('add two different files to session store', (t) => {
  const sessionID = 'lib-session-file-tracking-1';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/one.txt');
  lib.appendSessionFile(sessionID, '/repo/two.txt');

  const files = lib.getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/one.txt', '/repo/two.txt']);
});

test('dedupe repeated file paths in session store', (t) => {
  const sessionID = 'lib-session-file-tracking-2';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/repeat.txt');
  lib.appendSessionFile(sessionID, '/repo/repeat.txt');

  const files = lib.getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/repeat.txt']);
});

test('keep only the latest 10 files when more are appended', (t) => {
  const sessionID = 'lib-session-file-tracking-3';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  for (let i = 1; i <= 11; i++) {
    lib.appendSessionFile(sessionID, `/repo/file-${i}.txt`);
  }

  const files = lib.getSessionFiles(sessionID);
  assert.strictEqual(files.length, 10);
  assert.deepStrictEqual(files, [
    '/repo/file-2.txt',
    '/repo/file-3.txt',
    '/repo/file-4.txt',
    '/repo/file-5.txt',
    '/repo/file-6.txt',
    '/repo/file-7.txt',
    '/repo/file-8.txt',
    '/repo/file-9.txt',
    '/repo/file-10.txt',
    '/repo/file-11.txt',
  ]);
});

test('appendSessionFile no-op behavior is detectable', (t) => {
  const sessionID = 'lib-session-file-tracking-4';
  t.after(() => cleanup(sessionID));

  cleanup(sessionID);

  lib.appendSessionFile(sessionID, '/repo/important.txt');
  const files = lib.getSessionFiles(sessionID);
  assert.deepStrictEqual(files, ['/repo/important.txt']);
});
