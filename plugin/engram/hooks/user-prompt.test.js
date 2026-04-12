const assert = require('node:assert/strict');
const test = require('node:test');

const { buildSearchRequest } = require('./user-prompt');

test('buildSearchRequest includes files_being_edited when provided', () => {
  const request = buildSearchRequest('engram', 'fix auth bug', '/repo', ['/repo/a.go', '/repo/b.go']);

  assert.equal(request.project, 'engram');
  assert.equal(request.query, 'fix auth bug');
  assert.equal(request.cwd, '/repo');
  assert.deepEqual(request.files_being_edited, ['/repo/a.go', '/repo/b.go']);
});

test('buildSearchRequest omits files_being_edited when empty', () => {
  const request = buildSearchRequest('engram', 'fix auth bug', '/repo', []);

  assert.equal(request.project, 'engram');
  assert.equal(request.query, 'fix auth bug');
  assert.equal(request.cwd, '/repo');
  assert.equal(Object.prototype.hasOwnProperty.call(request, 'files_being_edited'), false);
});
