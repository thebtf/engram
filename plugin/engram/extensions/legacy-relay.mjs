import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { TextDecoder } from 'node:util';

export const RELAY_PROTOCOL = 'engram-legacy-relay/1';
export const ADAPTER_REVISION = 'omp-hap-01b/1';
export const RELAY_ROUTES = Object.freeze([
  'IDENTITY_REGISTRATION',
  'SESSION_START_CONTEXT',
  'AMBIENT_CANDIDATES',
]);
export const RELAY_NO_DELIVERY_REASONS = Object.freeze([
  'LOCATOR_MISSING',
  'DIAL_FAILED',
  'STALE_GENERATION',
  'BOOTSTRAP_UNAVAILABLE',
  'BOOTSTRAP_AMBIGUOUS',
  'CAPABILITY_INVALID',
  'DEADLINE_ELAPSED',
  'SERVER_UNAVAILABLE',
]);
export const RELAY_REJECTION_REASONS = Object.freeze([
  'ADAPTER_UNACCEPTED',
  'INVALID_REQUEST',
]);

export const INSTALLED_ARTIFACT_RELATIVE_PATHS = Object.freeze([
  'extensions/engram-memory.mjs',
  'extensions/legacy-relay.mjs',
]);

// SHA-256 domain bytes followed by lexically ordered, length-framed TCB files:
// u32be(UTF-8 relative-path length) | relative-path bytes |
// u64be(file length) | file bytes. Relative names, never install paths, bind the claim.
export const INSTALLED_ARTIFACT_DIGEST_ALGORITHM =
  'sha256(utf8("engram-hap-01b-extension-tcb/1\\0") || for each lexical relative path: u32be(path bytes) || path bytes || u64be(file bytes) || file bytes)';

const digestDomain = Buffer.from('engram-hap-01b-extension-tcb/1\0', 'utf8');
const locatorFileName = 'engram-hap-01b.locator.json';
const maxLocatorBytes = 4096;
const maxFrameBytes = 64 * 1024;
const maxOpaqueReferenceBytes = 256;
const maxDescriptorBytes = 16 * 1024;
const maxSessionStartPayloadBytes = 48 * 1024;
const maxAdditionalContextBytes = 12 * 1024;
const maxArtifactFileBytes = 1024 * 1024;
const capabilityEntropyBytes = 32;
const requestEntropyBytes = 16;

const routeSet = new Set(RELAY_ROUTES);
const noDeliveryReasonSet = new Set(RELAY_NO_DELIVERY_REASONS);
const rejectionReasonSet = new Set(RELAY_REJECTION_REASONS);
const descriptorKeys = new Set([
  'version',
  'anchor_project_id',
  'name',
  'scope',
  'normalized_git_remotes',
  'legacy_identifiers',
  'client_instance_id',
]);
const legacyIdentifierKeys = new Set(['scheme', 'value', 'provenance']);
const legacyIdentifierSchemes = new Set([
  'anchor_v3',
  'binding_v2',
  'git_remote_relative_v2',
  'git_hash_v2',
  'path_hash_v1',
  'legacy_slug',
  'non_git_anchor_v2',
  'manual_alias',
]);
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const sha256 = /^[a-f0-9]{64}$/;
const base64Url = /^[A-Za-z0-9_-]+$/;
const utf8Decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });

const extensionDirectory = path.dirname(fileURLToPath(import.meta.url));
const defaultArtifactFiles = Object.freeze([
  Object.freeze({
    relativePath: 'extensions/engram-memory.mjs',
    filePath: path.join(extensionDirectory, 'engram-memory.mjs'),
  }),
  Object.freeze({
    relativePath: 'extensions/legacy-relay.mjs',
    filePath: path.join(extensionDirectory, 'legacy-relay.mjs'),
  }),
]);

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype;
}

function validUtf8String(value) {
  return typeof value === 'string' && Buffer.from(value, 'utf8').toString('utf8') === value;
}

function decodeUtf8(bytes) {
  if (!Buffer.isBuffer(bytes) || bytes.length === 0 ||
    (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf)) {
    return null;
  }
  try {
    return utf8Decoder.decode(bytes);
  } catch {
    return null;
  }
}

function skipWhitespace(text, index) {
  while (index < text.length && /[\t\n\r ]/.test(text[index])) index += 1;
  return index;
}

function scanString(text, index) {
  if (text[index] !== '"') return -1;
  for (let cursor = index + 1; cursor < text.length; cursor += 1) {
    const code = text.charCodeAt(cursor);
    if (code < 0x20) return -1;
    if (text[cursor] === '\\') {
      cursor += 1;
      continue;
    }
    if (text[cursor] === '"') return cursor + 1;
  }
  return -1;
}

function scanValue(text, index) {
  if (index >= text.length) return -1;
  if (text[index] === '"') return scanString(text, index);
  if (text[index] !== '{' && text[index] !== '[') {
    let cursor = index;
    while (cursor < text.length && !',}]'.includes(text[cursor])) cursor += 1;
    return cursor > index ? cursor : -1;
  }

  const closing = text[index] === '{' ? '}' : ']';
  const stack = [closing];
  for (let cursor = index + 1; cursor < text.length; cursor += 1) {
    if (text[cursor] === '"') {
      cursor = scanString(text, cursor) - 1;
      if (cursor < index) return -1;
      continue;
    }
    if (text[cursor] === '{') stack.push('}');
    else if (text[cursor] === '[') stack.push(']');
    else if (text[cursor] === '}' || text[cursor] === ']') {
      if (stack.pop() !== text[cursor]) return -1;
      if (stack.length === 0) return cursor + 1;
    }
  }
  return -1;
}

function parseExactJSONObject(text, expectedKeys) {
  if (typeof text !== 'string') return null;
  let decoded;
  try {
    decoded = JSON.parse(text);
  } catch {
    return null;
  }
  if (!isPlainObject(decoded)) return null;

  let cursor = skipWhitespace(text, 0);
  if (text[cursor] !== '{') return null;
  cursor = skipWhitespace(text, cursor + 1);
  const rawValues = new Map();
  if (text[cursor] === '}') {
    cursor = skipWhitespace(text, cursor + 1);
  } else {
    for (; ;) {
      const keyStart = cursor;
      const keyEnd = scanString(text, keyStart);
      if (keyEnd < 0) return null;
      let key;
      try {
        key = JSON.parse(text.slice(keyStart, keyEnd));
      } catch {
        return null;
      }
      if (typeof key !== 'string' || rawValues.has(key)) return null;
      cursor = skipWhitespace(text, keyEnd);
      if (text[cursor] !== ':') return null;
      cursor = skipWhitespace(text, cursor + 1);
      const valueStart = cursor;
      const valueEnd = scanValue(text, valueStart);
      if (valueEnd < 0) return null;
      rawValues.set(key, text.slice(valueStart, valueEnd));
      cursor = skipWhitespace(text, valueEnd);
      if (text[cursor] === '}') {
        cursor = skipWhitespace(text, cursor + 1);
        break;
      }
      if (text[cursor] !== ',') return null;
      cursor = skipWhitespace(text, cursor + 1);
    }
  }
  if (cursor !== text.length || rawValues.size !== Object.keys(decoded).length) return null;
  if (expectedKeys) {
    if (rawValues.size !== expectedKeys.size) return null;
    for (const key of rawValues.keys()) if (!expectedKeys.has(key)) return null;
  }
  return { value: decoded, rawValues };
}

function exactObject(value, expectedKeys) {
  if (!isPlainObject(value)) return false;
  const keys = Object.keys(value);
  return keys.length === expectedKeys.size && keys.every((key) => expectedKeys.has(key));
}

function byteLength(value) {
  return typeof value === 'string' ? Buffer.byteLength(value, 'utf8') : 0;
}

function isDescriptorText(value, maxBytes) {
  return validUtf8String(value) && value !== '' && byteLength(value) <= maxBytes &&
    value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
}

export function isOpaqueReference(value) {
  return validUtf8String(value) && value !== '' && byteLength(value) <= maxOpaqueReferenceBytes &&
    value.trim() === value && !/[\p{Cc}\p{Z}\s/\\@]/u.test(value);
}

function hasUnsafeUserInfoShape(value) {
  if (typeof value !== 'string') return true;
  const schemeMatch = /^([a-z][a-z0-9+.-]*):\/\/([^/?#]*)@/i.exec(value);
  if (schemeMatch) return schemeMatch[1].toLowerCase() !== 'ssh' || schemeMatch[2] !== 'git';
  return /^[^@/\s]+:[^@/\s]*@/.test(value);
}

function validClientInstanceID(value) {
  return isDescriptorText(value, maxOpaqueReferenceBytes) && !/[ /\\@]/u.test(value) &&
    !hasUnsafeUserInfoShape(value) && !/^[a-z][a-z0-9+.-]*:/i.test(value);
}

function validDescriptor(value) {
  if (!exactObject(value, descriptorKeys) || value.version !== 3 ||
    !uuid.test(value.anchor_project_id) || !isDescriptorText(value.name, 256) ||
    (value.scope !== 'repository' && value.scope !== 'directory') ||
    !validClientInstanceID(value.client_instance_id) ||
    !Array.isArray(value.normalized_git_remotes) || value.normalized_git_remotes.length > 32 ||
    !Array.isArray(value.legacy_identifiers) || value.legacy_identifiers.length > 32) {
    return false;
  }
  if (!value.normalized_git_remotes.every((entry) => isDescriptorText(entry, 2048) && !hasUnsafeUserInfoShape(entry))) {
    return false;
  }
  return value.legacy_identifiers.every((entry) => exactObject(entry, legacyIdentifierKeys) &&
    legacyIdentifierSchemes.has(entry.scheme) && isDescriptorText(entry.value, 2048) &&
    isDescriptorText(entry.provenance, 2048) && !hasUnsafeUserInfoShape(entry.value) &&
    !hasUnsafeUserInfoShape(entry.provenance) &&
    (entry.scheme !== 'manual_alias' || !/[ \t\r\n]/u.test(entry.value)));
}

export function normalizeProjectIdentityV3Descriptor(value) {
  if (!isPlainObject(value)) return null;
  let text;
  try {
    text = JSON.stringify(value);
  } catch {
    return null;
  }
  if (byteLength(text) === 0 || byteLength(text) > maxDescriptorBytes) return null;
  const parsed = parseExactJSONObject(text, descriptorKeys);
  return parsed && validDescriptor(parsed.value) ? parsed.value : null;
}

function validCapability(value) {
  if (typeof value !== 'string' || value.length !== 43 || value.includes('=') || !base64Url.test(value)) return false;
  try {
    const decoded = Buffer.from(value, 'base64url');
    return decoded.length === capabilityEntropyBytes && decoded.toString('base64url') === value;
  } catch {
    return false;
  }
}

function validAdditionalContext(value) {
  return validUtf8String(value) && byteLength(value) <= maxAdditionalContextBytes;
}

function validEndpoint(endpoint, platform = process.platform) {
  if (!validUtf8String(endpoint) || endpoint === '' || byteLength(endpoint) > maxLocatorBytes ||
    endpoint.trim() !== endpoint || /[\u0000-\u001f\u007f]/u.test(endpoint) || endpoint.includes('://')) {
    return false;
  }
  if (platform === 'win32') {
    return path.win32.isAbsolute(endpoint) && !/^\\\\[.?]\\pipe\\/iu.test(endpoint);
  }
  return path.posix.isAbsolute(endpoint);
}

export function legacyRelayDialEndpoint(endpoint, platform = process.platform) {
  if (!validEndpoint(endpoint, platform)) return '';
  if (platform !== 'win32') return endpoint;
  const digest = crypto.createHash('sha256').update(endpoint.toLowerCase(), 'utf8').digest('hex').slice(0, 32);
  return `\\\\.\\pipe\\mcp-mux-${digest}`;
}

function pathApi(platform) {
  return platform === 'win32' ? path.win32 : path.posix;
}

function absolutePath(value, platform) {
  return typeof value === 'string' && value !== '' && value.trim() === value &&
    !/[\u0000-\u001f\u007f]/u.test(value) && pathApi(platform).isAbsolute(value) ? value : '';
}

export function goUserCacheDir({ platform = process.platform, env = process.env } = {}) {
  const paths = pathApi(platform);
  if (platform === 'win32') return absolutePath(env.LOCALAPPDATA, platform);
  if (platform === 'darwin') {
    const home = absolutePath(env.HOME, platform);
    return home ? paths.join(home, 'Library', 'Caches') : '';
  }
  if (typeof env.XDG_CACHE_HOME === 'string' && env.XDG_CACHE_HOME !== '') {
    return absolutePath(env.XDG_CACHE_HOME, platform);
  }
  const home = absolutePath(env.HOME, platform);
  return home ? paths.join(home, '.cache') : '';
}

export function legacyRelayLocatorPath(options = {}) {
  const platform = options.platform ?? process.platform;
  const cacheDir = goUserCacheDir({ platform, env: options.env ?? process.env });
  return cacheDir ? pathApi(platform).join(cacheDir, 'engram', 'run', 'hap-01b', locatorFileName) : '';
}

export function parseLegacyRelayLocator(bytes, { platform = process.platform } = {}) {
  if (!Buffer.isBuffer(bytes) || bytes.length === 0 || bytes.length > maxLocatorBytes) return null;
  const text = decodeUtf8(bytes);
  const parsed = text === null ? null : parseExactJSONObject(text, new Set([
    'protocol',
    'daemon_generation',
    'endpoint',
  ]));
  if (!parsed) return null;
  const value = parsed.value;
  if (value.protocol !== RELAY_PROTOCOL || !isOpaqueReference(value.daemon_generation) ||
    !validEndpoint(value.endpoint, platform)) {
    return null;
  }
  return Object.freeze({
    protocol: RELAY_PROTOCOL,
    daemonGeneration: value.daemon_generation,
    endpoint: value.endpoint,
  });
}

function readBoundedLocator(locatorPath, fsImpl, platform) {
  if (typeof locatorPath !== 'string' || locatorPath === '') return null;
  let descriptor;
  try {
    descriptor = fsImpl.openSync(locatorPath, 'r');
    const metadata = fsImpl.fstatSync(descriptor);
    if (!metadata.isFile() || !Number.isSafeInteger(metadata.size) || metadata.size <= 0 ||
      metadata.size > maxLocatorBytes) {
      return null;
    }
    const bytes = Buffer.allocUnsafe(metadata.size);
    let offset = 0;
    while (offset < bytes.length) {
      const count = fsImpl.readSync(descriptor, bytes, offset, bytes.length - offset, null);
      if (!Number.isInteger(count) || count <= 0) return null;
      offset += count;
    }
    const extra = Buffer.allocUnsafe(1);
    if (fsImpl.readSync(descriptor, extra, 0, 1, null) !== 0) return null;
    return parseLegacyRelayLocator(bytes, { platform });
  } catch {
    return null;
  } finally {
    if (descriptor !== undefined) {
      try {
        fsImpl.closeSync(descriptor);
      } catch {
        // The locator is advisory discovery data; a close error cannot become delivery.
      }
    }
  }
}

function readArtifactFile(filePath, fsImpl) {
  let descriptor;
  try {
    descriptor = fsImpl.openSync(filePath, 'r');
    const metadata = fsImpl.fstatSync(descriptor);
    if (!metadata.isFile() || !Number.isSafeInteger(metadata.size) || metadata.size < 0 ||
      metadata.size > maxArtifactFileBytes) {
      return null;
    }
    const bytes = Buffer.allocUnsafe(metadata.size);
    let offset = 0;
    while (offset < bytes.length) {
      const count = fsImpl.readSync(descriptor, bytes, offset, bytes.length - offset, null);
      if (!Number.isInteger(count) || count <= 0) return null;
      offset += count;
    }
    const extra = Buffer.allocUnsafe(1);
    if (fsImpl.readSync(descriptor, extra, 0, 1, null) !== 0) return null;
    return bytes;
  } catch {
    return null;
  } finally {
    if (descriptor !== undefined) {
      try {
        fsImpl.closeSync(descriptor);
      } catch {
        // A read whose descriptor cannot close is not an attested artifact.
      }
    }
  }
}

export function calculateInstalledArtifactSha256(artifactFiles = defaultArtifactFiles, fsImpl = fs) {
  if (!Array.isArray(artifactFiles) || artifactFiles.length !== INSTALLED_ARTIFACT_RELATIVE_PATHS.length) return null;
  const files = artifactFiles.map((entry) => ({ ...entry })).sort((left, right) => {
    const leftPath = String(left.relativePath);
    const rightPath = String(right.relativePath);
    return leftPath < rightPath ? -1 : leftPath > rightPath ? 1 : 0;
  });
  if (!files.every((entry, index) => entry.relativePath === INSTALLED_ARTIFACT_RELATIVE_PATHS[index] &&
    typeof entry.filePath === 'string' && entry.filePath !== '')) {
    return null;
  }

  try {
    const hash = crypto.createHash('sha256');
    hash.update(digestDomain);
    for (const entry of files) {
      const relativePathBytes = Buffer.from(entry.relativePath, 'utf8');
      const fileBytes = readArtifactFile(entry.filePath, fsImpl);
      if (!fileBytes || relativePathBytes.length === 0 || relativePathBytes.length > 0xffffffff) return null;
      const pathLength = Buffer.allocUnsafe(4);
      const fileLength = Buffer.allocUnsafe(8);
      pathLength.writeUInt32BE(relativePathBytes.length);
      fileLength.writeBigUInt64BE(BigInt(fileBytes.length));
      hash.update(pathLength);
      hash.update(relativePathBytes);
      hash.update(fileLength);
      hash.update(fileBytes);
    }
    return hash.digest('hex');
  } catch {
    return null;
  }
}

function noDelivery(route, reason) {
  return Object.freeze({
    kind: 'NO_DELIVERY',
    route,
    reason: noDeliveryReasonSet.has(reason) ? reason : 'SERVER_UNAVAILABLE',
  });
}

function remainingMilliseconds(deadlineUnixMs, now) {
  if (!Number.isSafeInteger(deadlineUnixMs) || deadlineUnixMs <= 0) return 0;
  const current = now();
  return Number.isFinite(current) ? Math.max(0, deadlineUnixMs - Math.floor(current)) : 0;
}

function normalizedRequestBody(route, body) {
  if (!isPlainObject(body)) return null;
  if (route === 'IDENTITY_REGISTRATION') {
    const expected = new Set(['hostSessionRef', 'projectIdentityV3']);
    const descriptor = normalizeProjectIdentityV3Descriptor(body.projectIdentityV3);
    return exactObject(body, expected) && isOpaqueReference(body.hostSessionRef) && descriptor ? {
      hostSessionRef: body.hostSessionRef,
      projectIdentityV3: descriptor,
    } : null;
  }
  if (route === 'SESSION_START_CONTEXT') {
    const expected = new Set(['hostSessionRef', 'sessionCapability']);
    return exactObject(body, expected) && isOpaqueReference(body.hostSessionRef) && validCapability(body.sessionCapability) ? {
      hostSessionRef: body.hostSessionRef,
      sessionCapability: body.sessionCapability,
    } : null;
  }
  if (route === 'AMBIENT_CANDIDATES') {
    const expected = new Set(['hostSessionRef', 'sessionCapability', 'queryText']);
    return exactObject(body, expected) && isOpaqueReference(body.hostSessionRef) &&
      validCapability(body.sessionCapability) && validUtf8String(body.queryText) && body.queryText !== '' &&
      byteLength(body.queryText) <= maxAdditionalContextBytes ? {
      hostSessionRef: body.hostSessionRef,
      sessionCapability: body.sessionCapability,
      queryText: body.queryText,
    } : null;
  }
  return null;
}

function requestFrame(locator, route, body, deadlineUnixMs, artifactDigest, randomBytes, platform) {
  if (!locator || locator.protocol !== RELAY_PROTOCOL || !isOpaqueReference(locator.daemonGeneration) ||
    !validEndpoint(locator.endpoint, platform) || !routeSet.has(route) || !sha256.test(artifactDigest)) {
    return null;
  }
  const normalizedBody = normalizedRequestBody(route, body);
  if (!normalizedBody) return null;
  let random;
  try {
    random = Buffer.from(randomBytes(requestEntropyBytes));
  } catch {
    return null;
  }
  if (random.length !== requestEntropyBytes) return null;
  const frame = {
    protocol: RELAY_PROTOCOL,
    requestId: random.toString('base64url'),
    daemonGeneration: locator.daemonGeneration,
    adapter: {
      revision: ADAPTER_REVISION,
      installedArtifactSha256: artifactDigest,
    },
    route,
    deadlineUnixMs,
    body: normalizedBody,
  };
  let serialized;
  try {
    serialized = Buffer.from(JSON.stringify(frame), 'utf8');
  } catch {
    return null;
  }
  return serialized.length > 0 && serialized.length + 1 < maxFrameBytes ? { frame, serialized } : null;
}

function parseSessionPayload(raw) {
  if (typeof raw !== 'string' || byteLength(raw) === 0 || byteLength(raw) > maxSessionStartPayloadBytes) return null;
  const parsed = parseExactJSONObject(raw, null);
  return parsed ? parsed.value : null;
}

function parseRelayResponse(line, expected) {
  const text = decodeUtf8(line);
  const outer = text === null ? null : parseExactJSONObject(text, null);
  if (!outer) return null;
  const value = outer.value;
  if (value.protocol !== RELAY_PROTOCOL || value.requestId !== expected.requestId ||
    value.daemonGeneration !== expected.daemonGeneration || value.route !== expected.route ||
    !isOpaqueReference(value.requestId) || !isOpaqueReference(value.daemonGeneration) || !routeSet.has(value.route)) {
    return null;
  }

  const responseKeys = new Set(['protocol', 'requestId', 'daemonGeneration', 'route', 'kind']);
  if (value.kind === 'OK' && value.route === 'IDENTITY_REGISTRATION') {
    const expectedKeys = new Set([...responseKeys, 'sessionCapability', 'canonicalProjectRef']);
    if (!exactObject(value, expectedKeys) || !validCapability(value.sessionCapability) ||
      !isOpaqueReference(value.canonicalProjectRef)) {
      return null;
    }
    return Object.freeze({
      kind: 'OK',
      route: value.route,
      sessionCapability: value.sessionCapability,
      canonicalProjectRef: value.canonicalProjectRef,
    });
  }
  if (value.kind === 'OK' && value.route === 'SESSION_START_CONTEXT') {
    const expectedKeys = new Set([...responseKeys, 'payload']);
    const payload = exactObject(value, expectedKeys) ? parseSessionPayload(outer.rawValues.get('payload')) : null;
    return payload ? Object.freeze({ kind: 'OK', route: value.route, payload }) : null;
  }
  if (value.kind === 'OK' && value.route === 'AMBIENT_CANDIDATES') {
    const expectedKeys = new Set([...responseKeys, 'additionalContext']);
    return exactObject(value, expectedKeys) && validAdditionalContext(value.additionalContext) ? Object.freeze({
      kind: 'OK',
      route: value.route,
      additionalContext: value.additionalContext,
    }) : null;
  }
  if (value.kind === 'NO_DELIVERY') {
    const expectedKeys = new Set([...responseKeys, 'reason']);
    return exactObject(value, expectedKeys) && noDeliveryReasonSet.has(value.reason) ? Object.freeze({
      kind: 'NO_DELIVERY',
      route: value.route,
      reason: value.reason,
    }) : null;
  }
  if (value.kind === 'REJECTED') {
    const expectedKeys = new Set([...responseKeys, 'reason']);
    return exactObject(value, expectedKeys) && rejectionReasonSet.has(value.reason) ? Object.freeze({
      kind: 'REJECTED',
      route: value.route,
      reason: value.reason,
    }) : null;
  }
  return null;
}

function connectOnce({ locator, route, body, deadlineUnixMs, artifactDigest, netImpl, now, randomBytes, platform }) {
  const request = requestFrame(locator, route, body, deadlineUnixMs, artifactDigest, randomBytes, platform);
  if (!request) return Promise.resolve(noDelivery(route, 'SERVER_UNAVAILABLE'));
  const remaining = remainingMilliseconds(deadlineUnixMs, now);
  if (remaining <= 0) return Promise.resolve(noDelivery(route, 'DEADLINE_ELAPSED'));

  return new Promise((resolve) => {
    let socket;
    let complete = false;
    let line = null;
    let received = 0;
    const responseBuffer = Buffer.allocUnsafe(maxFrameBytes);
    const finish = (result) => {
      if (complete) return;
      complete = true;
      clearTimeout(timer);
      if (socket) {
        try {
          socket.destroy();
        } catch {
          // A closed socket is already in the required terminal state.
        }
      }
      resolve(result);
    };
    const finishFromLine = () => {
      const parsed = line && parseRelayResponse(line, request.frame);
      finish(parsed || noDelivery(route, 'SERVER_UNAVAILABLE'));
    };
    const timer = setTimeout(() => finish(noDelivery(route, 'DEADLINE_ELAPSED')), remaining);

    try {
      const endpoint = legacyRelayDialEndpoint(locator.endpoint, platform);
      if (!endpoint) {
        finish(noDelivery(route, 'DIAL_FAILED'));
        return;
      }
      socket = netImpl.createConnection({ path: endpoint });
    } catch {
      finish(noDelivery(route, 'DIAL_FAILED'));
      return;
    }

    socket.once('error', () => finish(noDelivery(route, 'DIAL_FAILED')));
    socket.once('connect', () => {
      if (complete || remainingMilliseconds(deadlineUnixMs, now) <= 0) {
        finish(noDelivery(route, 'DEADLINE_ELAPSED'));
        return;
      }
      try {
        const frame = Buffer.allocUnsafe(request.serialized.length + 1);
        request.serialized.copy(frame);
        frame[frame.length - 1] = 0x0a;
        socket.end(frame);
      } catch {
        finish(noDelivery(route, 'DIAL_FAILED'));
      }
    });
    socket.on('data', (chunk) => {
      if (complete) return;
      if (line !== null || !Buffer.isBuffer(chunk) || received + chunk.length > maxFrameBytes) {
        finish(noDelivery(route, 'SERVER_UNAVAILABLE'));
        return;
      }
      chunk.copy(responseBuffer, received);
      received += chunk.length;
      const newline = responseBuffer.indexOf(0x0a, 0, received);
      if (newline < 0) return;
      if (newline === 0 || received !== newline + 1) {
        finish(noDelivery(route, 'SERVER_UNAVAILABLE'));
        return;
      }
      line = responseBuffer.subarray(0, newline);
    });
    socket.once('end', () => {
      if (!complete) finishFromLine();
    });
    socket.once('close', (hadError) => {
      if (!complete) {
        if (hadError) finish(noDelivery(route, 'DIAL_FAILED'));
        else finishFromLine();
      }
    });
  });
}

export function createLegacyRelay(options = {}) {
  const fsImpl = options.fs ?? fs;
  const netImpl = options.net ?? net;
  const now = options.now ?? Date.now;
  const platform = options.platform ?? process.platform;
  const environment = options.env ?? process.env;
  const locatorPath = options.locatorPath ?? (() => legacyRelayLocatorPath({ platform, env: environment }));
  const artifactFiles = options.artifactFiles ?? defaultArtifactFiles;
  const randomBytes = options.randomBytes ?? crypto.randomBytes;
  const artifactDigestSource = options.artifactDigest ?? (() => calculateInstalledArtifactSha256(artifactFiles, fsImpl));
  let cachedArtifactDigest;

  function selectedArtifactDigest() {
    if (cachedArtifactDigest !== undefined) return cachedArtifactDigest;
    try {
      cachedArtifactDigest = typeof artifactDigestSource === 'function'
        ? artifactDigestSource()
        : artifactDigestSource;
    } catch {
      cachedArtifactDigest = null;
    }
    return cachedArtifactDigest;
  }

  function discoverLocator() {
    let selectedPath;
    try {
      selectedPath = locatorPath();
    } catch {
      return null;
    }
    return readBoundedLocator(selectedPath, fsImpl, platform);
  }

  return Object.freeze({
    async call(route, body, deadlineUnixMs) {
      if (!routeSet.has(route)) return noDelivery(route, 'SERVER_UNAVAILABLE');
      if (remainingMilliseconds(deadlineUnixMs, now) <= 0) return noDelivery(route, 'DEADLINE_ELAPSED');
      const digest = selectedArtifactDigest();
      if (typeof digest !== 'string' || !sha256.test(digest)) return noDelivery(route, 'SERVER_UNAVAILABLE');

      let locator = discoverLocator();
      if (!locator) return noDelivery(route, 'LOCATOR_MISSING');
      for (let attempt = 0; attempt < 2; attempt += 1) {
        if (remainingMilliseconds(deadlineUnixMs, now) <= 0) return noDelivery(route, 'DEADLINE_ELAPSED');
        const result = await connectOnce({
          locator,
          route,
          body,
          deadlineUnixMs,
          artifactDigest: digest,
          netImpl,
          now,
          randomBytes,
          platform,
        });
        if (result.kind !== 'NO_DELIVERY' || result.reason !== 'STALE_GENERATION' || attempt !== 0) return result;
        if (remainingMilliseconds(deadlineUnixMs, now) <= 0) return noDelivery(route, 'DEADLINE_ELAPSED');
        locator = discoverLocator();
        if (!locator) return noDelivery(route, 'LOCATOR_MISSING');
      }
      return noDelivery(route, 'STALE_GENERATION');
    },
  });
}

export const legacyRelay = createLegacyRelay();
