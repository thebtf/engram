import crypto from 'node:crypto';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const lib = require('../hooks/lib.js');
const { buildSessionStartContext } = require('../hooks/session-start.js');
const { fetchAmbientAdditionalContext } = require('../hooks/user-prompt.js');

const sessionStartTimeoutMs = 5000;
const ambientTimeoutMs = 200;
const hiddenContextLimit = 12000;

function stringField(...values) {
  return values.find((value) => typeof value === 'string' && value !== '') || '';
}

function eventContext(event = {}, ctx = {}) {
  const cwd = stringField(event.cwd, event.workspace, ctx.cwd, ctx.workspace);
  let sessionID = stringField(event.sessionId, event.session_id, ctx.sessionId, ctx.session_id);
  if (!sessionID) {
    try {
      sessionID = stringField(ctx.sessionManager?.getSessionId?.());
    } catch {
      return null;
    }
  }
  return cwd && sessionID ? { cwd, sessionID } : null;
}

function boundedContext(value) {
  return typeof value === 'string' && value.length <= hiddenContextLimit ? value : '';
}

function hiddenMessage(content) {
  return {
    customType: 'engram-memory',
    content,
    display: false,
    attribution: 'agent',
  };
}

function deadlineController(timeoutMs) {
  const controller = new AbortController();
  const deadlineAt = performance.now() + timeoutMs;
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  return {
    signal: controller.signal,
    remaining() {
      if (controller.signal.aborted) return 0;
      const remaining = Math.floor(deadlineAt - performance.now());
      if (remaining <= 0) controller.abort();
      return Math.max(0, remaining);
    },
    dispose() {
      clearTimeout(timeout);
    },
  };
}

function untilAborted(signal, operation) {
  if (signal.aborted) return Promise.resolve(null);
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener('abort', onAbort);
      resolve(value);
    };
    const onAbort = () => finish(null);
    signal.addEventListener('abort', onAbort, { once: true });
    Promise.resolve().then(() => signal.aborted ? null : operation()).then(finish, () => finish(null));
  });
}

async function validateProject(event, ctx, deadline) {
  if (deadline.remaining() <= 0) return null;
  const identity = eventContext(event, ctx);
  if (!identity) return null;
  const projectIdentity = await lib.resolveHookProjectIdentityV2(identity.cwd, {
    signal: deadline.signal,
    timeoutMs: deadline.remaining(),
  });
  if (deadline.remaining() <= 0) return null;
  const projectContext = {
    Project: projectIdentity.git_remote
      ? crypto.createHash('sha256').update(`${projectIdentity.git_remote}/${projectIdentity.relative_path}`).digest('hex').slice(0, 8)
      : crypto.createHash('sha256').update(path.resolve(identity.cwd)).digest('hex').slice(0, 6),
    LegacyProject: lib.LegacyProjectID(identity.cwd),
    GitRemote: projectIdentity.git_remote,
    RelativePath: projectIdentity.relative_path,
    ProjectIdentityV2: projectIdentity,
  };
  await lib.registerProjectIdentityV2(projectContext, undefined, {
    signal: deadline.signal,
    timeoutMs: deadline.remaining(),
  });
  if (deadline.remaining() <= 0) return null;
  return { project: projectContext.Project, sessionID: identity.sessionID };
}

async function sessionStartMessage(event, ctx, timeoutMs = sessionStartTimeoutMs) {
  const deadline = deadlineController(Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : sessionStartTimeoutMs);
  try {
    const config = await untilAborted(deadline.signal, () => lib.resolveEngramRuntimeConfig({ signal: deadline.signal }));
    if (!config || deadline.remaining() <= 0 || config.quiet || !config.serverURL || !config.token) return null;
    const scope = await untilAborted(deadline.signal, () => validateProject(event, ctx, deadline));
    const remaining = deadline.remaining();
    if (!scope || remaining <= 0) return null;
    const payload = await untilAborted(deadline.signal, () => lib.requestPost(
      '/api/context/session-start',
      { project: scope.project, session_id: scope.sessionID },
      remaining,
      { signal: deadline.signal },
    ));
    if (!payload || deadline.remaining() <= 0) return null;
    const content = boundedContext(buildSessionStartContext(payload, scope.project, { maxLength: hiddenContextLimit }));
    return !content || deadline.remaining() <= 0 ? null : hiddenMessage(content);
  } finally {
    deadline.dispose();
  }
}

async function ambientMessage(event, ctx) {
  const deadline = deadlineController(ambientTimeoutMs);
  try {
    const config = await untilAborted(deadline.signal, () => lib.resolveEngramRuntimeConfig({ signal: deadline.signal }));
    if (!config || deadline.remaining() <= 0 || config.quiet || !config.serverURL || !config.token) return null;
    const scope = await untilAborted(deadline.signal, () => validateProject(event, ctx, deadline));
    const prompt = stringField(event.prompt, event.userMessage, event.user_message, ctx.prompt);
    const remaining = deadline.remaining();
    if (!scope || !prompt || remaining <= 0) return null;
    const content = boundedContext(await untilAborted(deadline.signal, () => fetchAmbientAdditionalContext(
      scope.project,
      scope.sessionID,
      prompt,
      remaining,
      { signal: deadline.signal },
    )));
    return deadline.signal.aborted || !content ? null : hiddenMessage(content);
  } finally {
    deadline.dispose();
  }
}

export default function engramMemory(pi) {
  pi.on('session_start', async (event, ctx) => {
    const message = await sessionStartMessage(event, ctx);
    if (message) pi.sendMessage(message, { deliverAs: 'nextTurn' });
  });
  pi.on('before_agent_start', async (event, ctx) => {
    const message = await ambientMessage(event, ctx);
    return message ? { message } : undefined;
  });
}

export { ambientMessage, hiddenMessage, sessionStartMessage };
