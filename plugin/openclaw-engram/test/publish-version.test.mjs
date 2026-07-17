import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  assertPublishVersionAdvances,
  compareStableVersions,
  scriptPath,
} from '../scripts/check-publish-version.mjs';

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('package and OpenClaw descriptor versions stay aligned', () => {
  const packageVersion = JSON.parse(fs.readFileSync(path.join(packageRoot, 'package.json'), 'utf8')).version;
  const descriptorVersion = JSON.parse(fs.readFileSync(path.join(packageRoot, 'openclaw.plugin.json'), 'utf8')).version;
  assert.equal(packageVersion, descriptorVersion);
});

test('stable semantic versions compare by major, minor, and patch', () => {
  assert.equal(compareStableVersions('3.8.0', '3.7.5'), 1);
  assert.equal(compareStableVersions('4.0.0', '3.99.99'), 1);
  assert.equal(compareStableVersions('3.7.6', '3.7.5'), 1);
  assert.equal(compareStableVersions('3.7.5', '3.7.5'), 0);
  assert.equal(compareStableVersions('3.7.4', '3.7.5'), -1);
});

test('publish gate rejects equal, older, and non-stable versions', () => {
  assert.throws(
    () => assertPublishVersionAdvances('3.7.5', '3.7.5'),
    /OPENCLAW_PUBLISH_VERSION_NOT_ADVANCED/,
  );
  assert.throws(
    () => assertPublishVersionAdvances('3.7.4', '3.7.5'),
    /OPENCLAW_PUBLISH_VERSION_NOT_ADVANCED/,
  );
  assert.throws(
    () => assertPublishVersionAdvances('3.8.0-rc.1', '3.7.5'),
    /OPENCLAW_PUBLISH_VERSION_INVALID/,
  );
});

test('publish gate CLI fails closed unless local is newer than npm', () => {
  const allowed = spawnSync(process.execPath, [scriptPath, '3.8.0', '3.7.5'], {
    encoding: 'utf8',
    windowsHide: true,
  });
  assert.equal(allowed.status, 0, allowed.stderr);
  assert.match(allowed.stdout, /OPENCLAW_PUBLISH_VERSION_OK: 3\.8\.0 > 3\.7\.5/);

  const blocked = spawnSync(process.execPath, [scriptPath, '3.7.5', '3.7.5'], {
    encoding: 'utf8',
    windowsHide: true,
  });
  assert.equal(blocked.status, 1, blocked.stderr);
  assert.match(blocked.stderr, /OPENCLAW_PUBLISH_VERSION_NOT_ADVANCED/);
});
