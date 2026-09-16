import { createRequire } from 'node:module';
import path from 'node:path';
import {
  isOpaqueReference,
  legacyRelay,
  normalizeProjectIdentityV3Descriptor,
} from './legacy-relay.mjs';

const require = createRequire(import.meta.url);
const lib = require('../hooks/lib.js');
const { buildSessionStartContext } = require('../hooks/session-start.js');

const sessionStartTimeoutMs = 5000;
const ambientTimeoutMs = 500;
const hiddenContextLimit = 12000;
const descriptorCacheLimit = 64;

function stringField(...values) {
  return values.find((value) => typeof value === 'string' && value !== '') || '';
}

function isValidUtf8(value) {
  return typeof value === 'string' && Buffer.from(value, 'utf8').toString('utf8') === value;
}

function boundedPrompt(event = {}, ctx = {}) {
  const prompt = stringField(event.prompt, event.userMessage, event.user_message, ctx.prompt);
  return isValidUtf8(prompt) && Buffer.byteLength(prompt, 'utf8') <= hiddenContextLimit ? prompt : '';
}

function callbackFacts(event = {}, ctx = {}, requireCwd) {
  const hostSessionRef = stringField(event.sessionId, event.session_id, ctx.sessionId, ctx.session_id);
  let resolvedSessionRef = hostSessionRef;
  if (!resolvedSessionRef) {
    try {
      resolvedSessionRef = stringField(ctx.sessionManager?.getSessionId?.());
    } catch {
      return null;
    }
  }
  if (!isOpaqueReference(resolvedSessionRef)) return null;
  if (!requireCwd) return { hostSessionRef: resolvedSessionRef };
  const cwd = stringField(event.cwd, event.workspace, ctx.cwd, ctx.workspace);
  return cwd ? { cwd, hostSessionRef: resolvedSessionRef } : null;
}
function canonicalCwd(value) {
  try {
    return path.resolve(value);
  } catch {
    return '';
  }
}

function sameCwd(left, right) {
  const canonicalLeft = canonicalCwd(left);
  const canonicalRight = canonicalCwd(right);
  if (!canonicalLeft || !canonicalRight) return false;
  return process.platform === 'win32'
    ? canonicalLeft.toLowerCase() === canonicalRight.toLowerCase()
    : canonicalLeft === canonicalRight;
}


function callbackDeadlineUnixMs(timeoutMs, now) {
  const startedAt = Math.floor(now());
  return Number.isSafeInteger(startedAt) && Number.isInteger(timeoutMs) && timeoutMs > 0
    ? startedAt + timeoutMs
    : 0;
}

function deadlineActive(deadlineUnixMs, now) {
  return Number.isSafeInteger(deadlineUnixMs) && deadlineUnixMs > Math.floor(now());
}

function boundedContext(value) {
  return isValidUtf8(value) && value !== '' && Buffer.byteLength(value, 'utf8') <= hiddenContextLimit ? value : '';
}

function hiddenMessage(content) {
  return {
    customType: 'engram-memory',
    content,
    display: false,
    attribution: 'agent',
  };
}

function isIdentityResponse(value) {
  return value?.kind === 'OK' && value.route === 'IDENTITY_REGISTRATION' &&
    typeof value.sessionCapability === 'string' && typeof value.canonicalProjectRef === 'string';
}

function isSessionContextResponse(value) {
  return value?.kind === 'OK' && value.route === 'SESSION_START_CONTEXT' &&
    value.payload && typeof value.payload === 'object' && !Array.isArray(value.payload);
}

function isAmbientResponse(value) {
  return value?.kind === 'OK' && value.route === 'AMBIENT_CANDIDATES' &&
    typeof value.additionalContext === 'string';
}

export function createEngramMemoryExtension(options = {}) {
  const relay = options.relay ?? legacyRelay;
  const now = options.now ?? Date.now;
  const resolveDescriptor = options.resolveHookProjectDescriptorV3 ?? lib.resolveHookProjectDescriptorV3;
  const isQuiet = options.isQuiet ?? lib.isQuietMode;
  const cacheLimit = Number.isInteger(options.descriptorCacheLimit) && options.descriptorCacheLimit > 0
    ? options.descriptorCacheLimit
    : descriptorCacheLimit;
  const descriptors = new Map();

  function rememberDescriptor(hostSessionRef, cwd, projectIdentityV3) {
    const canonicalScope = canonicalCwd(cwd);
    if (!canonicalScope) return false;
    descriptors.delete(hostSessionRef);
    descriptors.set(hostSessionRef, Object.freeze({ cwd: canonicalScope, projectIdentityV3 }));
    while (descriptors.size > cacheLimit) descriptors.delete(descriptors.keys().next().value);
    return true;
  }

  function sessionIdentity(event, ctx, deadlineUnixMs) {
    const facts = callbackFacts(event, ctx, true);
    const clientInstanceID = lib.getEngramConfig().clientInstanceID;
    if (!facts || !clientInstanceID) return null;
    let projectIdentityV3;
    try {
      projectIdentityV3 = normalizeProjectIdentityV3Descriptor(resolveDescriptor(facts.cwd, clientInstanceID));
    } catch {
      return null;
    }
    if (!projectIdentityV3) return null;
    if (!deadlineActive(deadlineUnixMs, now)) return null;
    if (!rememberDescriptor(facts.hostSessionRef, facts.cwd, projectIdentityV3)) return null;
    return { hostSessionRef: facts.hostSessionRef, projectIdentityV3 };
  }

  function ambientIdentity(event, ctx) {
    const facts = callbackFacts(event, ctx, true);
    const clientInstanceID = lib.getEngramConfig().clientInstanceID;
    if (!facts || !clientInstanceID) return null;
    const cachedEntry = descriptors.get(facts.hostSessionRef);
    const cached = normalizeProjectIdentityV3Descriptor(cachedEntry?.projectIdentityV3);
    if (!cached || cached.client_instance_id !== clientInstanceID || !sameCwd(cachedEntry.cwd, facts.cwd)) {
      descriptors.delete(facts.hostSessionRef);
      return null;
    }
    return { hostSessionRef: facts.hostSessionRef, projectIdentityV3: cached };
  }

  async function call(route, body, deadlineUnixMs) {
    if (!deadlineActive(deadlineUnixMs, now)) return null;
    try {
      const result = await relay.call(route, body, deadlineUnixMs);
      return deadlineActive(deadlineUnixMs, now) ? result : null;
    } catch {
      return null;
    }
  }

  async function sessionStartMessage(event, ctx, timeoutMs = sessionStartTimeoutMs) {
    const deadlineUnixMs = callbackDeadlineUnixMs(
      Number.isInteger(timeoutMs) && timeoutMs > 0 ? timeoutMs : sessionStartTimeoutMs,
      now,
    );
    if (!deadlineActive(deadlineUnixMs, now) || isQuiet()) return null;
    const identity = sessionIdentity(event, ctx, deadlineUnixMs);
    if (!identity || !deadlineActive(deadlineUnixMs, now)) return null;
    const registered = await call('IDENTITY_REGISTRATION', identity, deadlineUnixMs);
    if (!isIdentityResponse(registered)) return null;
    const context = await call('SESSION_START_CONTEXT', {
      hostSessionRef: identity.hostSessionRef,
      sessionCapability: registered.sessionCapability,
    }, deadlineUnixMs);
    if (!isSessionContextResponse(context) || !deadlineActive(deadlineUnixMs, now)) return null;
    let content;
    try {
      content = boundedContext(buildSessionStartContext(context.payload, registered.canonicalProjectRef, {
        maxLength: hiddenContextLimit,
      }));
    } catch {
      return null;
    }
    return content && deadlineActive(deadlineUnixMs, now) ? hiddenMessage(content) : null;
  }

  async function ambientMessage(event, ctx) {
    const deadlineUnixMs = callbackDeadlineUnixMs(ambientTimeoutMs, now);
    if (!deadlineActive(deadlineUnixMs, now) || isQuiet()) return null;
    const identity = ambientIdentity(event, ctx);
    const queryText = boundedPrompt(event, ctx);
    if (!identity || !queryText || !deadlineActive(deadlineUnixMs, now)) return null;
    const registered = await call('IDENTITY_REGISTRATION', identity, deadlineUnixMs);
    if (!isIdentityResponse(registered)) return null;
    const ambient = await call('AMBIENT_CANDIDATES', {
      hostSessionRef: identity.hostSessionRef,
      sessionCapability: registered.sessionCapability,
      queryText,
    }, deadlineUnixMs);
    const content = isAmbientResponse(ambient) ? boundedContext(ambient.additionalContext) : '';
    return content && deadlineActive(deadlineUnixMs, now) ? hiddenMessage(content) : null;
  }

  function install(pi) {
    pi.on('session_start', async (event, ctx) => {
      const message = await sessionStartMessage(event, ctx);
      if (message) pi.sendMessage(message, { deliverAs: 'nextTurn' });
    });
    pi.on('before_agent_start', async (event, ctx) => {
      const message = await ambientMessage(event, ctx);
      return message ? { message } : undefined;
    });
  }

  return Object.freeze({ ambientMessage, install, sessionStartMessage });
}

const liveExtension = createEngramMemoryExtension();

export default function engramMemory(pi) {
  liveExtension.install(pi);
}

function sessionStartMessage(...args) {
  return liveExtension.sessionStartMessage(...args);
}

function ambientMessage(...args) {
  return liveExtension.ambientMessage(...args);
}

export { ambientMessage, hiddenMessage, sessionStartMessage };
