import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const STABLE_SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

function parseStableVersion(value, label) {
  const match = typeof value === 'string' ? STABLE_SEMVER.exec(value) : null;
  if (!match) {
    throw new Error(`OPENCLAW_PUBLISH_VERSION_INVALID: ${label} must be a stable semantic version`);
  }
  return match.slice(1).map((part) => BigInt(part));
}

export function compareStableVersions(left, right) {
  const leftParts = parseStableVersion(left, 'local');
  const rightParts = parseStableVersion(right, 'remote');
  for (let index = 0; index < leftParts.length; index += 1) {
    if (leftParts[index] > rightParts[index]) return 1;
    if (leftParts[index] < rightParts[index]) return -1;
  }
  return 0;
}

export function assertPublishVersionAdvances(local, remote) {
  if (compareStableVersions(local, remote) <= 0) {
    throw new Error(
      `OPENCLAW_PUBLISH_VERSION_NOT_ADVANCED: local ${local} must be greater than npm ${remote}`,
    );
  }
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : '';
const isMain = invokedPath !== '' && import.meta.url === pathToFileURL(invokedPath).href;
if (isMain) {
  const [local, remote] = process.argv.slice(2);
  try {
    assertPublishVersionAdvances(local, remote);
    process.stdout.write(`OPENCLAW_PUBLISH_VERSION_OK: ${local} > ${remote}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

export const scriptPath = fileURLToPath(import.meta.url);
