'use strict';

const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const ANCHOR_KEYS = new Set(['version', 'project_id', 'name', 'scope']);
const DESCRIPTOR_KEYS = new Set([
  'anchor',
  'version',
  'anchor_project_id',
  'name',
  'scope',
  'normalized_git_remotes',
  'legacy_identifiers',
  'client_instance_id',
]);
const WIRE_DESCRIPTOR_KEYS = new Set([...DESCRIPTOR_KEYS].filter((key) => key !== 'anchor'));
const LEGACY_IDENTIFIER_KEYS = new Set(['scheme', 'value', 'provenance']);
const LEGACY_SCHEMES = new Set([
  'anchor_v3',
  'binding_v2',
  'git_remote_relative_v2',
  'git_hash_v2',
  'path_hash_v1',
  'legacy_slug',
  'non_git_anchor_v2',
  'manual_alias',
]);
const CONTROL = /\p{Cc}/u;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const HOST = /^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i;

function anchorInvalid(reason) {
  return new Error(`PROJECT_ANCHOR_INVALID: ${reason}`);
}

function descriptorInvalid(reason) {
  return new Error(`PROJECT_DESCRIPTOR_INVALID: ${reason}`);
}

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isSafeText(value, maxLength = 2048) {
  return typeof value === 'string' && value.length > 0 && value.length <= maxLength &&
    value.trim() === value && !CONTROL.test(value);
}

function hasCredentialShape(value) {
  if (typeof value !== 'string') return false;
  const urlUserInfo = /^([a-z][a-z0-9+.-]*):\/\/([^/?#]*)@/i.exec(value);
  if (urlUserInfo) return urlUserInfo[1].toLowerCase() !== 'ssh' || urlUserInfo[2] !== 'git';
  return /^[^@/\s]+:[^@/\s]*@/.test(value);
}

function validateClientInstanceIDV3(value) {
  if (!isSafeText(value, 256) || /[\s/\\@]/u.test(value) || hasCredentialShape(value) ||
    /^[a-z][a-z0-9+.-]*:/i.test(value)) {
    throw descriptorInvalid('client instance ID is malformed');
  }
  return value;
}

function parseProjectAnchorV3(raw) {
  if (!isPlainObject(raw)) throw anchorInvalid('anchor must be an object');
  const keys = Object.keys(raw);
  if (keys.length !== ANCHOR_KEYS.size || keys.some((key) => !ANCHOR_KEYS.has(key))) {
    throw anchorInvalid('anchor fields are invalid');
  }
  if (raw.version !== 3 || !UUID.test(raw.project_id) || !isSafeText(raw.name, 256) ||
    (raw.scope !== 'repository' && raw.scope !== 'directory')) {
    throw anchorInvalid('anchor fields are malformed');
  }
  return raw;
}

function samePath(left, right) {
  const normalizedLeft = path.normalize(left);
  const normalizedRight = path.normalize(right);
  return process.platform === 'win32'
    ? normalizedLeft.toLowerCase() === normalizedRight.toLowerCase()
    : normalizedLeft === normalizedRight;
}

function repositoryTracksAnchor(scopeRoot) {
  try {
    const gitRoot = execFileSync('git', ['-C', scopeRoot, 'rev-parse', '--show-toplevel'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
      timeout: 2000,
      windowsHide: true,
    }).trim();
    if (!samePath(gitRoot, scopeRoot)) return false;
    execFileSync('git', ['-C', scopeRoot, 'ls-files', '--error-unmatch', '--', '.engram-project'], {
      stdio: 'ignore',
      timeout: 2000,
      windowsHide: true,
    });
    return true;
  } catch (_) {
    return false;
  }
}

function discoverProjectAnchorV3(scopeRoot, selectedScope) {
  if (typeof scopeRoot !== 'string' || scopeRoot === '') throw anchorInvalid('scope root is required');
  if (selectedScope != null && selectedScope !== 'repository' && selectedScope !== 'directory') {
    throw anchorInvalid('selected scope is invalid');
  }
  const root = path.resolve(scopeRoot);
  const anchorPath = path.join(root, '.engram-project');
  let bytes;
  try {
    bytes = fs.readFileSync(anchorPath, 'utf8');
  } catch (error) {
    if (error && error.code === 'ENOENT') return null;
    throw anchorInvalid('anchor cannot be read');
  }
  let anchor;
  try {
    anchor = parseProjectAnchorV3(JSON.parse(bytes));
  } catch (error) {
    if (error && /^PROJECT_ANCHOR_INVALID:/.test(error.message)) throw error;
    throw anchorInvalid('anchor JSON is malformed');
  }
  if (selectedScope != null && anchor.scope !== selectedScope) {
    throw new Error('PROJECT_SCOPE_MISMATCH: selected scope differs from anchor');
  }
  if (anchor.scope === 'repository' && !repositoryTracksAnchor(root)) {
    throw anchorInvalid('repository anchor must be tracked at the selected repository root');
  }
  return anchor;
}

function emptyRemote(disposition) {
  return { disposition, value: '' };
}

function normalizeRemoteParts(host, port, remotePath) {
  if (!HOST.test(host) || (port !== '' && (!/^\d+$/.test(port) || Number(port) > 65535)) ||
    !isSafeText(remotePath) || remotePath.includes('\\') || remotePath.includes('?') || remotePath.includes('#')) {
    return null;
  }
  const parts = remotePath.split('/').filter(Boolean);
  if (parts.length === 0 || parts.some((part) => part === '.' || part === '..' || CONTROL.test(part))) return null;
  if (parts.at(-1).endsWith('.git')) parts[parts.length - 1] = parts.at(-1).slice(0, -4);
  if (!parts.at(-1)) parts.pop();
  if (parts.length === 0) return null;
  return `${host.toLowerCase()}${port === '' ? '' : `:${port}`}/${parts.join('/')}`;
}

function normalizeGitRemoteV3(observation) {
  if (isPlainObject(observation)) {
    if (observation.form === 'credential_bearing_url' ||
      observation.form === 'credential_bearing_scp_multi_colon') return emptyRemote('refused');
    if (!Object.hasOwn(observation, 'source')) return emptyRemote('omitted');
    observation = observation.source;
  }
  if (typeof observation !== 'string' || observation === '' || CONTROL.test(observation)) {
    return emptyRemote('omitted');
  }
  if (hasCredentialShape(observation)) return emptyRemote('refused');
  if (observation !== observation.trim() || /^file:/i.test(observation) ||
    /^[a-z]:[\\/]/i.test(observation) || observation.startsWith('/')) return emptyRemote('omitted');

  let match = /^https:\/\/(.+)$/i.exec(observation);
  if (match) {
    try {
      const url = new URL(observation);
      if (url.username || url.password || url.search || url.hash) return emptyRemote('refused');
      const value = normalizeRemoteParts(url.hostname, url.port, url.pathname);
      return value ? { disposition: 'normalized', value } : emptyRemote('omitted');
    } catch (_) {
      return emptyRemote('omitted');
    }
  }

  match = /^ssh:\/\/(.+)$/i.exec(observation);
  if (match) {
    try {
      const url = new URL(observation);
      if ((url.username && url.username !== 'git') || url.password || url.search || url.hash) {
        return emptyRemote('refused');
      }
      const value = normalizeRemoteParts(url.hostname, url.port, url.pathname);
      return value ? { disposition: 'normalized', value } : emptyRemote('omitted');
    } catch (_) {
      return emptyRemote('omitted');
    }
  }

  match = /^(?:([^@/\s]+)@)?([^:/\s]+):(.+)$/.exec(observation);
  if (match) {
    const [, user, host, remotePath] = match;
    if (user && user !== 'git') return emptyRemote('refused');
    const value = normalizeRemoteParts(host, '', remotePath);
    return value ? { disposition: 'normalized', value } : emptyRemote('omitted');
  }
  return emptyRemote('omitted');
}

function isCanonicalRemote(value) {
  if (!isSafeText(value) || hasCredentialShape(value)) return false;
  const match = /^([^/:]+)(?::(\d+))?\/(.+)$/.exec(value);
  return Boolean(match && normalizeRemoteParts(match[1], match[2] || '', match[3]) === value);
}

function validateLegacyIdentifiers(identifiers) {
  if (!Array.isArray(identifiers)) throw descriptorInvalid('legacy identifiers must be an array');
  for (const identifier of identifiers) {
    if (!isPlainObject(identifier) || Object.keys(identifier).length !== LEGACY_IDENTIFIER_KEYS.size ||
      Object.keys(identifier).some((key) => !LEGACY_IDENTIFIER_KEYS.has(key)) ||
      !LEGACY_SCHEMES.has(identifier.scheme) || !isSafeText(identifier.value) ||
      (identifier.scheme === 'manual_alias' && /\s/u.test(identifier.value)) ||
      !isSafeText(identifier.provenance) || hasCredentialShape(identifier.value) ||
      hasCredentialShape(identifier.provenance)) {
      throw descriptorInvalid('legacy identifier is malformed');
    }
  }
}

function buildProjectIdentityV3(input) {
  if (!isPlainObject(input)) throw descriptorInvalid('descriptor input must be an object');
  if (Object.hasOwn(input, 'project_key')) {
    throw new Error('PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN: client project key is forbidden');
  }
  if (Object.keys(input).some((key) => !DESCRIPTOR_KEYS.has(key))) {
    throw descriptorInvalid('descriptor fields are invalid');
  }
  const anchor = parseProjectAnchorV3(input.anchor);
  if (input.version != null && input.version !== 3) {
    throw new Error('PROJECT_DESCRIPTOR_UNSUPPORTED: descriptor version is unsupported');
  }
  if (input.anchor_project_id != null && input.anchor_project_id !== anchor.project_id) {
    throw descriptorInvalid('anchor project ID differs from anchor');
  }
  if (input.name != null && input.name !== anchor.name) {
    throw descriptorInvalid('descriptor name differs from anchor');
  }
  if (input.scope != null && input.scope !== anchor.scope) {
    throw new Error('PROJECT_SCOPE_MISMATCH: descriptor scope differs from anchor');
  }
  if (!Array.isArray(input.normalized_git_remotes)) {
    throw descriptorInvalid('descriptor evidence is malformed');
  }
  validateClientInstanceIDV3(input.client_instance_id);
  if (!input.normalized_git_remotes.every(isCanonicalRemote)) {
    throw descriptorInvalid('normalized git remote is malformed');
  }
  validateLegacyIdentifiers(input.legacy_identifiers);
  return {
    version: 3,
    anchor_project_id: anchor.project_id,
    name: anchor.name,
    scope: anchor.scope,
    normalized_git_remotes: input.normalized_git_remotes,
    legacy_identifiers: input.legacy_identifiers,
    client_instance_id: input.client_instance_id,
  };
}

function validateProjectDescriptorV3(descriptor) {
  if (!isPlainObject(descriptor)) throw descriptorInvalid('descriptor must be an object');
  if (Object.hasOwn(descriptor, 'project_key')) {
    throw new Error('PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN: client project key is forbidden');
  }
  const keys = Object.keys(descriptor);
  if (keys.length !== WIRE_DESCRIPTOR_KEYS.size || keys.some((key) => !WIRE_DESCRIPTOR_KEYS.has(key))) {
    throw descriptorInvalid('descriptor fields are invalid');
  }
  return buildProjectIdentityV3({
    ...descriptor,
    anchor: {
      version: 3,
      project_id: descriptor.anchor_project_id,
      name: descriptor.name,
      scope: descriptor.scope,
    },
  });
}

module.exports = {
  parseProjectAnchorV3,
  discoverProjectAnchorV3,
  normalizeGitRemoteV3,
  buildProjectIdentityV3,
  validateClientInstanceIDV3,
  validateProjectDescriptorV3,
};
