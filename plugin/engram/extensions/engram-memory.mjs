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

function withinDeadline(operation, timeoutMs) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(value);
    };
    const timer = setTimeout(() => finish(null), timeoutMs);
    Promise.resolve().then(operation).then(finish, () => finish(null));
  });
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

async function validateProject(event, ctx, deadline = null) {
  if (deadline?.expired()) return null;
  const identity = eventContext(event, ctx);
  if (!identity) return null;

  const projectIdentity = lib.resolveProjectIdentityV2(identity.cwd);
  const projectContext = {
    Project: lib.ProjectIDWithName(identity.cwd),
    LegacyProject: lib.LegacyProjectID(identity.cwd),
    GitRemote: projectIdentity.git_remote,
    RelativePath: projectIdentity.relative_path,
    ProjectIdentityV2: projectIdentity,
  };
  if (deadline?.expired()) return null;
  if (deadline) {
    await lib.registerProjectIdentityV2(projectContext, undefined, { signal: deadline.signal });
  } else {
    await lib.registerProjectIdentityV2(projectContext);
  }
  if (deadline?.expired()) return null;
  return { project: projectContext.Project, sessionID: identity.sessionID };
}

async function sessionStartMessage(event, ctx, timeoutMs = sessionStartTimeoutMs) {
  if (lib.isQuietMode()) return null;
  const config = lib.getEngramConfig();
  if (!config.serverURL || !config.token) return null;

  const budgetMs = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : sessionStartTimeoutMs;
  const controller = new AbortController();
  const deadlineAt = performance.now() + budgetMs;
  const timeout = setTimeout(() => controller.abort(), budgetMs);
  const deadline = {
    signal: controller.signal,
    expired() {
      if (controller.signal.aborted) return true;
      if (performance.now() < deadlineAt) return false;
      controller.abort();
      return true;
    },
  };
  const remainingBudget = () => {
    if (deadline.expired()) return 0;
    return Math.floor(deadlineAt - performance.now());
  };

  try {
    const scope = await untilAborted(controller.signal, () => validateProject(event, ctx, deadline));
    const remaining = remainingBudget();
    if (!scope || remaining <= 0) return null;

    const payload = await untilAborted(
      controller.signal,
      () => deadline.expired()
        ? null
        : lib.requestPost(
          '/api/context/session-start',
          { project: scope.project, session_id: scope.sessionID },
          remaining,
          { signal: controller.signal },
        ),
    );
    if (!payload || deadline.expired()) return null;
    const content = boundedContext(buildSessionStartContext(payload, scope.project, { maxLength: hiddenContextLimit }));
    return deadline.expired() || !content ? null : hiddenMessage(content);
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
    controller.abort();
  }
}

async function ambientMessage(event, ctx) {
  if (lib.isQuietMode()) return null;
  return withinDeadline(async () => {
    const config = lib.getEngramConfig();
    if (!config.serverURL || !config.token) return null;

    const scope = await validateProject(event, ctx);
    const prompt = stringField(event.prompt, event.userMessage, event.user_message, ctx.prompt);
    if (!scope || !prompt) return null;
    const content = boundedContext(await fetchAmbientAdditionalContext(scope.project, scope.sessionID, prompt));
    return content ? hiddenMessage(content) : null;
  }, ambientTimeoutMs);
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
