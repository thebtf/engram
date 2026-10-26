import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const lib = require('../hooks/lib.js');
const { buildSessionStartContext } = require('../hooks/session-start.js');
const { buildAmbientRequest, formatAmbientAdditionalContext } = require('../hooks/user-prompt.js');
const { DOWNLOAD_DEADLINE_MS } = require('../scripts/ensure-binary.js');
const { prepareHookClient } = require('../scripts/run-engram.js');
const hiddenContextLimit = 12000;

function stringField(...values) {
  return values.find(value => typeof value === 'string' && value !== '') || '';
}

function callbackFacts(event = {}, ctx = {}) {
  let session = stringField(event.sessionId, event.session_id, ctx.sessionId, ctx.session_id);
  if (!session) {
    try { session = stringField(ctx.sessionManager?.getSessionId?.()); } catch { return null; }
  }
  const cwd = stringField(event.cwd, event.workspace, ctx.cwd, ctx.workspace);
  return cwd && /^[^\s\p{Cc}/\\@]{1,256}$/u.test(session) ? { session, cwd } : null;
}

export function hiddenMessage(content) {
  return { customType: 'engram-memory', content, display: false, attribution: 'agent' };
}

export function createEngramMemoryExtension(options = {}) {
  const resolveContext = options.resolveHookProjectContext ?? lib.resolveHookProjectContext;
  const requestPost = options.requestPost ?? lib.requestPost;
  const runtimeConfig = options.resolveEngramRuntimeConfig ?? lib.resolveEngramRuntimeConfig;
  const isQuiet = options.isQuiet ?? lib.isQuietMode;
  const prepare = options.prepareHookClient ?? prepareHookClient;
  const diagnostic = options.onDiagnostic ?? (message => process.stderr.write(`[engram] ${message}\n`));
  const unavailable = () => diagnostic('Trusted hook client readiness unavailable; check the plugin cache and start a fresh ordinary session.');
  let readinessSkipped = false;

  async function message(event, ctx, timeoutMs, ambient) {
    if (isQuiet()) return null;
    const facts = callbackFacts(event, ctx);
    if (!facts) return null;
    const prompt = ambient ? stringField(event?.prompt, event?.userMessage, event?.user_message, ctx?.prompt) : '';
    if (ambient && (!prompt || Buffer.byteLength(prompt) > hiddenContextLimit || Buffer.from(prompt, 'utf8').toString('utf8') !== prompt)) return null;
    const controller = new AbortController();
    const deadline = Date.now() + timeoutMs;
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const config = await runtimeConfig({ signal: controller.signal });
      if (!config.serverURL || config.quiet || controller.signal.aborted || Date.now() >= deadline) return null;
      if (readinessSkipped) { unavailable(); return null; }
      const context = await resolveContext(facts.cwd, {
        signal: controller.signal, deadlineAt: deadline, timeoutMs: Math.max(1, deadline - Date.now()), acquire: false,
        env: { ...process.env, ENGRAM_URL: config.serverURL, ENGRAM_TOKEN: config.token },
      });
      if (controller.signal.aborted || Date.now() >= deadline) return null;
      const payload = await requestPost(
        ambient ? '/api/hooks/ambient-candidates' : '/api/context/session-start',
        ambient ? buildAmbientRequest(context.Project, facts.session, prompt) : { project: context.Project, session_id: facts.session },
        Math.max(1, deadline - Date.now()),
        { signal: controller.signal, serverURL: config.serverURL, token: config.token },
      );
      const content = ambient ? formatAmbientAdditionalContext(payload) : buildSessionStartContext(payload, context.Project, { maxLength: hiddenContextLimit });
      if (controller.signal.aborted || Date.now() >= deadline || typeof content !== 'string' || !content || Buffer.byteLength(content) > hiddenContextLimit) return null;
      return hiddenMessage(content);
    } catch (error) {
      if (/HOOK_CLIENT_UNAVAILABLE|resolved client failed final integrity verification/.test(error?.message ?? '')) unavailable();
      return null;
    } finally { clearTimeout(timer); }
  }

  const sessionStartMessage = (event, ctx, timeoutMs = 5000) => message(event, ctx, timeoutMs, false);
  const ambientMessage = (event, ctx) => message(event, ctx, 500, true);
  async function install(pi) {
    const controller = new AbortController();
    const deadlineAt = Date.now() + DOWNLOAD_DEADLINE_MS;
    let onAbort, preparation, unregister;
    const cancelled = new Promise((_, reject) => {
      onAbort = () => reject(new Error('client readiness cancelled'));
      controller.signal.addEventListener('abort', onAbort, { once: true });
    });
    const timer = setTimeout(() => controller.abort(), DOWNLOAD_DEADLINE_MS);
    try {
      unregister = options.registerCleanup?.('engram-client-readiness', async () => {
        controller.abort();
        await preparation?.catch(() => { });
      });
      preparation = (async () => {
        if (controller.signal.aborted) throw new Error('client readiness cancelled');
        if (isQuiet()) { readinessSkipped = true; return; }
        const config = await runtimeConfig({ signal: controller.signal });
        if (controller.signal.aborted || Date.now() >= deadlineAt) throw new Error('client readiness expired');
        readinessSkipped = !config.serverURL || config.quiet;
        if (!readinessSkipped) {
          await prepare({ signal: controller.signal, deadlineAt, timeoutMs: deadlineAt - Date.now() });
        }
        if (controller.signal.aborted || Date.now() >= deadlineAt) throw new Error('client readiness expired');
      })();
      await Promise.race([preparation, cancelled]);
    } catch {
      throw new Error('[engram] Trusted client readiness failed or was cancelled. Check release connectivity and the plugin cache, then start a fresh ordinary session.');
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener('abort', onAbort);
      unregister?.();
    }
    pi.on('session_start', async (event, ctx) => {
      const result = await sessionStartMessage(event, ctx);
      if (result) pi.sendMessage(result, { deliverAs: 'nextTurn' });
    });
    pi.on('before_agent_start', async (event, ctx) => {
      const result = await ambientMessage(event, ctx);
      return result ? { message: result } : undefined;
    });
  }
  return Object.freeze({ install, sessionStartMessage, ambientMessage });
}

export default async function engramMemory(pi) {
  const { postmortem } = await import('@oh-my-pi/pi-utils');
  await createEngramMemoryExtension({ registerCleanup: postmortem.register }).install(pi);
}
export const sessionStartMessage = (...args) => createEngramMemoryExtension().sessionStartMessage(...args);
export const ambientMessage = (...args) => createEngramMemoryExtension().ambientMessage(...args);
