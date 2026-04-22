#!/usr/bin/env node
'use strict';

const lib = require('./lib');

function getString(value) {
  return typeof value === 'string' ? value : '';
}

function escapeXmlTags(text) {
  if (typeof text !== 'string') return '';
  return text.replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function formatFactsLine(items) {
  if (!Array.isArray(items) || items.length === 0) return '';

  let out = 'Key facts:\n';
  for (const fact of items) {
    if (typeof fact === 'string' && fact !== '') {
      out += `- ${escapeXmlTags(fact)}\n`;
    }
  }

  return out;
}

function buildInjectURL(project, cwd, sessionID, legacyProject, gitRemote, relativePath, filesBeingEdited) {
  let injectURL = `/api/context/inject?project=${encodeURIComponent(project)}&cwd=${encodeURIComponent(cwd)}`;
  if (sessionID) {
    injectURL += `&session_id=${encodeURIComponent(sessionID)}`;
  }
  if (legacyProject && legacyProject !== project) {
    injectURL += `&legacy_project=${encodeURIComponent(legacyProject)}`;
    injectURL += `&git_remote=${encodeURIComponent(gitRemote)}`;
    injectURL += `&relative_path=${encodeURIComponent(relativePath)}`;
  }
  if (Array.isArray(filesBeingEdited)) {
    for (const filePath of filesBeingEdited) {
      if (typeof filePath === 'string' && filePath !== '') {
        injectURL += `&files_being_edited=${encodeURIComponent(filePath)}`;
      }
    }
  }
  return injectURL;
}

function formatProjectBriefingBlock(projectBriefing) {
  const briefing = escapeXmlTags(getString(projectBriefing)).trim();
  if (briefing === '') {
    return '';
  }
  return '<project-briefing>\n'
    + '# Project Briefing\n'
    + briefing
    + '\n</project-briefing>\n';
}

function formatBehaviorRulesBlock(rules) {
  if (!Array.isArray(rules) || rules.length === 0) {
    return '';
  }

  let block = '<user-behavior-rules>\n';
  block += '# Behavioral Rules (Always Active)\n';
  block += 'These rules are injected unconditionally. Follow them in every session.\n\n';

  for (const rule of rules) {
    if (!rule || typeof rule !== 'object') continue;
    const title = escapeXmlTags(getString(rule.title) || getString(rule.content));
    const narrative = escapeXmlTags(getString(rule.narrative) || getString(rule.content));
    if (title !== '') {
      block += `## ${title}\n`;
    }
    if (narrative !== '') {
      block += `${narrative}\n`;
    }
    block += formatFactsLine(rule.facts);
    block += '\n';
  }

  block += '</user-behavior-rules>\n';
  return block;
}

function formatMemoriesBlock(memories) {
  if (!Array.isArray(memories) || memories.length === 0) {
    return '';
  }

  let block = '<engram-static-memories>\n';
  block += '# Recent Memory\n';
  block += 'Static session-start memories from Engram. Prefer using these before rediscovering context.\n\n';

  for (const memory of memories) {
    if (!memory || typeof memory !== 'object') continue;
    const content = escapeXmlTags(getString(memory.content));
    if (content === '') continue;
    block += `- ${content}\n`;
  }

  block += '</engram-static-memories>\n';
  return block;
}

function buildSessionStartContext(payload, project) {
  const issues = payload && Array.isArray(payload.issues) ? payload.issues : [];
  const rules = payload && Array.isArray(payload.rules) ? payload.rules : [];
  const memories = payload && Array.isArray(payload.memories) ? payload.memories : [];
  const blocks = [];

  if (issues.length > 0) {
    blocks.push(lib.formatIssuesBlock(issues, project));
  }
  const behaviorRulesBlock = formatBehaviorRulesBlock(rules);
  if (behaviorRulesBlock) {
    blocks.push(behaviorRulesBlock.trimEnd());
  }
  const memoriesBlock = formatMemoriesBlock(memories);
  if (memoriesBlock) {
    blocks.push(memoriesBlock.trimEnd());
  }

  return blocks.filter(Boolean).join('\n') + (blocks.length > 0 ? '\n' : '');
}

function getSessionStartCachePayload(project) {
  const cachePath = lib.getSessionStartCachePath(project);
  const payload = lib.readJSONFile(cachePath);
  if (!payload || typeof payload !== 'object') {
    return { cachePath, payload: null };
  }
  return { cachePath, payload };
}

function cacheSessionStartPayload(project, payload) {
  const cachePath = lib.getSessionStartCachePath(project);
  if (!cachePath) {
    return;
  }
  lib.writeJSONFile(cachePath, payload);
}

function formatStaleCacheBanner(generatedAt) {
  const stamp = getString(generatedAt).trim();
  const suffix = stamp !== '' ? ` Cached payload generated at ${stamp}.` : '';
  return `<engram-session-start-stale>\nWARNING: Engram session-start context is stale because live fetch failed.${suffix}\n</engram-session-start-stale>\n`;
}

function formatNoCacheBanner() {
  return '<engram-session-start-unavailable>\nWARNING: Engram session-start context is unavailable and no cache is present. Continuing without injected static context.\n</engram-session-start-unavailable>\n';
}

async function fetchSessionStartPayload(project) {
  return lib.requestGet(`/api/context/session-start?project=${encodeURIComponent(project)}`, 5000);
}

function buildCachedSessionStartPayload(overrides = {}) {
  return {
    issues: [],
    rules: [],
    memories: [],
    generated_at: new Date(0).toISOString(),
    ...overrides,
  };
}

async function handleSessionStart(ctx, input) {
  if (!process.env.ENGRAM_URL) {
    return '<engram-setup>\nEngram plugin is installed but not configured.\nSet environment variables to connect to your Engram server:\n  export ENGRAM_URL=http://your-server:37777/mcp\n  export ENGRAM_API_TOKEN=your-token\nThen restart Claude Code.\n</engram-setup>';
  }

  const project = typeof ctx.Project === 'string' ? ctx.Project : '';

  // Crash-safe session tracking (gstack-insights FR-8)
  const sessionID = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  if (sessionID) {
    lib.createPendingMarker(sessionID);
  }

  // Check for stale markers from crashed sessions (>2h old)
  const staleMarkers = lib.getStaleMarkers();
  for (const marker of staleMarkers) {
    // Record crashed session as timeline observation (fire-and-forget)
    lib.requestPost('/api/store', {
      action: 'create',
      content: `Session ${marker.sessionId} crashed (no stop hook fired)`,
      type: 'timeline',
      project: project || 'unknown',
      tags: ['event:crashed', `session:${marker.sessionId}`, 'outcome:crashed'],
      agent_source: 'claude-code',
    }, 3000).catch(() => {});
  }

  // Record session start timeline event (fire-and-forget, non-blocking per Constitution #3)
  if (project) {
    lib.requestPost('/api/store', {
      action: 'create',
      content: `Session started on ${project}`,
      type: 'timeline',
      project,
      tags: ['event:started', `session:${sessionID || 'unknown'}`],
      agent_source: 'claude-code',
    }, 3000).catch(() => {});
  }

  const { cachePath, payload: cachedPayload } = getSessionStartCachePayload(project);

  try {
    const payload = await fetchSessionStartPayload(project);
    cacheSessionStartPayload(project, payload);

    const rules = Array.isArray(payload && payload.rules) ? payload.rules : [];
    const issues = Array.isArray(payload && payload.issues) ? payload.issues : [];
    const memories = Array.isArray(payload && payload.memories) ? payload.memories : [];

    if (issues.length > 0) {
      console.error(`[engram] Injecting ${issues.length} active issues for ${project}`);
      const openIds = issues.filter((issue) => issue && issue.status === 'open').map((issue) => issue.id);
      if (openIds.length > 0) {
        lib.requestPost('/api/issues/acknowledge', { ids: openIds }, 3000).catch(() => {});
      }
    }
    if (rules.length > 0) {
      console.error(`[engram] Injected ${rules.length} static behavioral rules`);
    }
    if (memories.length > 0) {
      console.error(`[engram] Injected ${memories.length} static memories`);
    }

    return buildSessionStartContext(payload, project);
  } catch (error) {
    console.error(`[engram] Warning: static session-start fetch failed: ${error.message}`);
    if (cachedPayload) {
      console.error(`[engram] Using cached session-start payload from ${cachePath}`);
      return formatStaleCacheBanner(cachedPayload.generated_at) + buildSessionStartContext(cachedPayload, project);
    }
    console.error('[engram] No cached session-start payload available');
    return formatNoCacheBanner();
  }
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('SessionStart', handleSessionStart);
  })();
}

module.exports = {
  buildInjectURL,
  buildCachedSessionStartPayload,
  formatProjectBriefingBlock,
  handleSessionStart,
};