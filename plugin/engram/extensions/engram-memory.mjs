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
  return typeof value === 'string' ? value.slice(0, hiddenContextLimit) : '';
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

async function validateProject(event, ctx) {
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
  await lib.registerProjectIdentityV2(projectContext);
  return { project: projectContext.Project, sessionID: identity.sessionID };
}

async function sessionStartMessage(event, ctx) {
  if (lib.isQuietMode()) return null;
  const config = lib.getEngramConfig();
  if (!config.serverURL || !config.token) return null;

  try {
    const scope = await validateProject(event, ctx);
    if (!scope) return null;
    const payload = await lib.requestPost(
      '/api/context/session-start',
      { project: scope.project, session_id: scope.sessionID },
      sessionStartTimeoutMs,
    );
    const content = boundedContext(buildSessionStartContext(payload, scope.project));
    return content ? hiddenMessage(content) : null;
  } catch {
    return null;
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
