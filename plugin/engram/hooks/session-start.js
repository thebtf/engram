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

async function handleSessionStart(ctx, input) {
  if (!process.env.ENGRAM_URL) {
    return '<engram-setup>\nEngram plugin is installed but not configured.\nSet environment variables to connect to your Engram server:\n  export ENGRAM_URL=http://your-server:37777/mcp\n  export ENGRAM_API_TOKEN=your-token\nThen restart Claude Code.\n</engram-setup>';
  }

  const cwd = typeof ctx.CWD === 'string' ? ctx.CWD : '';
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

  const legacyProject = typeof ctx.LegacyProject === 'string' ? ctx.LegacyProject : '';
  const gitRemote = typeof ctx.GitRemote === 'string' ? ctx.GitRemote : '';
  const relativePath = typeof ctx.RelativePath === 'string' ? ctx.RelativePath : '';

  const ccSessionID = typeof ctx.SessionID === 'string' ? ctx.SessionID : '';
  const filesBeingEdited = ccSessionID ? lib.getSessionFiles(ccSessionID) : [];
  const injectURL = buildInjectURL(
    project,
    cwd,
    ccSessionID,
    legacyProject,
    gitRemote,
    relativePath,
    filesBeingEdited,
  );

  // NOTE: inject GET is still performed — result.always_inject is needed for behavioral rules.
  // Noisy fields (observations, full_count, project_briefing, guidance) are fetched but NOT rendered.
  let result = {};
  try {
    result = await lib.requestGet(injectURL);
  } catch (error) {
    console.error(`[engram] Warning: context fetch failed: ${error.message}`);
    return '';
  }

  // Log strategy for diagnostics only (not rendered)
  if (result && result.strategy) {
    console.error(`[session-start] Injection strategy: ${result.strategy}`);
  }

  // Fetch open/acknowledged/reopened issues targeting this project (agent-issues FR-5)
  let issuesBlock = '';
  let resolvedIssuesBlock = '';
  if (project) {
    try {
      // Target issues: issues assigned to this project (open, acknowledged, reopened — NOT closed/rejected)
      const issuesResult = await lib.requestGet(
        `/api/issues?project=${encodeURIComponent(project)}&status=open,acknowledged,reopened&limit=10`
      );
      const issues = Array.isArray(issuesResult.issues) ? issuesResult.issues : [];
      if (issues.length > 0) {
        issuesBlock = lib.formatIssuesBlock(issues, project);
        console.error(`[engram] Injecting ${issues.length} active issues for ${project}`);

        // Auto-acknowledge: transition open → acknowledged (fire-and-forget, Constitution #3)
        const openIds = issues.filter(i => i.status === 'open').map(i => i.id);
        if (openIds.length > 0) {
          lib.requestPost('/api/issues/acknowledge', { ids: openIds }, 3000).catch(() => {});
        }
      }

      // Source notification: issues created BY this project that were resolved (lifecycle-v2 FR-1)
      const sevenDaysAgoMs = Date.now() - 7 * 24 * 60 * 60 * 1000;
      const resolvedResult = await lib.requestGet(
        `/api/issues?source_project=${encodeURIComponent(project)}&status=resolved&resolved_since=${sevenDaysAgoMs}&limit=5`
      );
      const resolvedIssues = Array.isArray(resolvedResult.issues) ? resolvedResult.issues : [];
      if (resolvedIssues.length > 0) {
        resolvedIssuesBlock = lib.formatResolvedIssuesBlock(resolvedIssues, project);
        console.error(`[engram] Injecting ${resolvedIssues.length} resolved issues from ${project}`);
      }
    } catch (err) {
      console.error(`[engram] Warning: issue fetch failed: ${err.message}`);
    }
  }

  // Always-inject tier: unconditional behavioral rules (FR-1, FR-6)
  const alwaysInject = Array.isArray(result.always_inject) ? result.always_inject : [];
  let contextBuilder = '';

  // Issues blocks come first (before behavioral rules and memory)
  if (issuesBlock) {
    contextBuilder += issuesBlock + '\n';
  }
  if (resolvedIssuesBlock) {
    contextBuilder += resolvedIssuesBlock + '\n';
  }
  if (alwaysInject.length > 0) {
    contextBuilder += '<user-behavior-rules>\n';
    contextBuilder += '# Behavioral Rules (Always Active)\n';
    contextBuilder += 'These rules are injected unconditionally. Follow them in every session.\n\n';
    for (const rule of alwaysInject) {
      if (!rule || typeof rule !== 'object') continue;
      const rTitle = escapeXmlTags(getString(rule.title));
      const rNarrative = escapeXmlTags(getString(rule.narrative));
      contextBuilder += `## ${rTitle}\n`;
      if (rNarrative !== '') {
        contextBuilder += `${rNarrative}\n`;
      }
      contextBuilder += formatFactsLine(rule.facts);
      contextBuilder += '\n';
    }
    contextBuilder += '</user-behavior-rules>\n';
    console.error(`[engram] Injected ${alwaysInject.length} always-inject behavioral rules`);
  }

  // NOTE: <engram-context>, <project-briefing>, and <engram-guidance> sections are intentionally
  // disabled (v4.4.1 tactical fix #16). They produced noise rather than relevant context.
  // The inject GET above is kept because result.always_inject still needs it.
  // Re-enable by restoring the rendering blocks once inject pipeline is redesigned (Phase 2).

  // Mark injected IDs — scoped to always_inject ONLY (fire-and-forget).
  // observations and guidance are not rendered so must not be logged as injected
  // (would produce false positives in citation tracking).
  const ids = [];
  for (const obs of alwaysInject) {
    if (obs && typeof obs === 'object' && typeof obs.id === 'number' && obs.id > 0) {
      ids.push(obs.id);
    }
  }
  if (ids.length > 0) {
    // Try to resolve Claude session ID to DB session ID for per-session tracking
    let dbSessionId = null;
    if (ctx.SessionID) {
      try {
        const sessionResult = await lib.requestGet(
          `/api/sessions?claudeSessionId=${encodeURIComponent(ctx.SessionID)}`,
          3000
        );
        const candidateId = Number(sessionResult && sessionResult.id);
        if (Number.isFinite(candidateId) && candidateId > 0) {
          dbSessionId = candidateId;
        }
      } catch {
        // Session may not exist yet — fall through to global-only tracking
      }
    }

    if (dbSessionId !== null) {
      // Per-session endpoint does dual-write: session table + global injection_count
      lib.requestPost(`/api/sessions/${dbSessionId}/mark-injected`, { ids }, 3000).catch((err) => {
        console.error(`[engram] session mark-injected failed: ${err.message}`);
      });
    } else {
      // Fallback: global-only tracking when session isn't in DB yet
      lib.requestPost('/api/observations/mark-injected', { ids }, 3000).catch((err) => {
        console.error(`[engram] mark-injected failed: ${err.message}`);
      });
    }
  }

  return contextBuilder;
}

if (require.main === module) {
  (async () => {
    await lib.RunHook('SessionStart', handleSessionStart);
  })();
}

module.exports = {
  buildInjectURL,
  formatProjectBriefingBlock,
  handleSessionStart,
};