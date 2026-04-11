const assert = require('node:assert/strict');
const test = require('node:test');

const { buildInjectURL, formatProjectBriefingBlock } = require('./session-start');

test('buildInjectURL appends files_being_edited as repeated query params', () => {
  const url = buildInjectURL(
    'engram',
    '/repo',
    'sess-1',
    '',
    '',
    '',
    ['/repo/a.go', '/repo/b.go']
  );

  assert.match(url, /files_being_edited=%2Frepo%2Fa\.go/);
  assert.match(url, /files_being_edited=%2Frepo%2Fb\.go/);
  assert.match(url, /session_id=sess-1/);
});

test('buildInjectURL omits files_being_edited when none are present', () => {
  const url = buildInjectURL('engram', '/repo', 'sess-1', '', '', '', []);
  assert.doesNotMatch(url, /files_being_edited=/);
});

test('formatProjectBriefingBlock renders XML block when content exists', () => {
  const block = formatProjectBriefingBlock('Active Work\n- Build briefing');
  assert.match(block, /<project-briefing>/);
  assert.match(block, /# Project Briefing/);
  assert.match(block, /Active Work/);
  assert.match(block, /<\/project-briefing>/);
});

test('formatProjectBriefingBlock returns empty string when content missing', () => {
  assert.equal(formatProjectBriefingBlock(''), '');
  assert.equal(formatProjectBriefingBlock(null), '');
});
