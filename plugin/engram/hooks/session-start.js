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

async function handleSessionStart(ctx, input) {
  if (!process.env.ENGRAM_URL) {
    return '<engram-setup>\nEngram plugin is installed but not configured.\nSet environment variables to connect to your Engram server:\n  export ENGRAM_URL=http://your-server:37777/mcp\n  export ENGRAM_API_TOKEN=your-token\nThen restart Claude Code.\n</engram-setup>';
  }

  const cwd = typeof ctx.CWD === 'string' ? ctx.CWD : '';
  const project = typeof ctx.Project === 'string' ? ctx.Project : '';

  const legacyProject = typeof ctx.LegacyProject === 'string' ? ctx.LegacyProject : '';
  const gitRemote = typeof ctx.GitRemote === 'string' ? ctx.GitRemote : '';
  const relativePath = typeof ctx.RelativePath === 'string' ? ctx.RelativePath : '';

  let injectURL = `/api/context/inject?project=${encodeURIComponent(project)}&cwd=${encodeURIComponent(cwd)}`;
  if (legacyProject && legacyProject !== project) {
    injectURL += `&legacy_project=${encodeURIComponent(legacyProject)}`;
    injectURL += `&git_remote=${encodeURIComponent(gitRemote)}`;
    injectURL += `&relative_path=${encodeURIComponent(relativePath)}`;
  }

  let result = {};
  try {
    result = await lib.requestGet(injectURL);
  } catch (error) {
    console.error(`[engram] Warning: context fetch failed: ${error.message}`);
    return '';
  }

  const observations = Array.isArray(result.results)
    ? result.results
    : Array.isArray(result.observations)
      ? result.observations
      : [];
  let fullCount = 25;
  const fullCountCandidate = Number(result.full_count);
  if (Number.isFinite(fullCountCandidate) && fullCountCandidate > 0) {
    fullCount = Math.floor(fullCountCandidate);
  }

  const detailedCount = Math.min(fullCount, observations.length);
  const condensedCount = Math.max(0, observations.length - fullCount);
  console.error(
    `[engram] Injecting ${observations.length} observations from project memory (${detailedCount} detailed, ${condensedCount} condensed)`
  );

  let contextBuilder = '<engram-context>\n';
  contextBuilder += `# Project Memory (${observations.length} observations)\n`;
  contextBuilder +=
    'Use this knowledge to answer questions without re-exploring the codebase.\n\n';

  for (let i = 0; i < observations.length; i++) {
    const observation = observations[i];
    if (!observation || typeof observation !== 'object') {
      continue;
    }

    const obsType = escapeXmlTags(getString(observation.type));
    const title = escapeXmlTags(getString(observation.title));
    const typeLabel = obsType.toUpperCase();
    const scopeTag = (typeof observation.scope === 'string' && observation.scope === 'global') ? ' [GLOBAL]' : '';

    if (i < fullCount) {
      const narrative = escapeXmlTags(getString(observation.narrative));
      contextBuilder += `## ${i + 1}. [${typeLabel}] ${title}${scopeTag}\n`;
      if (narrative !== '') {
        contextBuilder += `${narrative}\n`;
      }
      contextBuilder += formatFactsLine(observation.facts);
      contextBuilder += '\n';
    } else {
      const subtitle = escapeXmlTags(getString(observation.subtitle));
      if (subtitle !== '') {
        contextBuilder += `- [${typeLabel}] ${title}${scopeTag}: ${subtitle}\n`;
      } else {
        contextBuilder += `- [${typeLabel}] ${title}${scopeTag}\n`;
      }
    }
  }

  contextBuilder += '</engram-context>\n';

  // Render guidance block if server provides guidance observations
  const guidance = Array.isArray(result.guidance) ? result.guidance : [];
  if (guidance.length > 0) {
    contextBuilder += '<engram-guidance>\n';
    contextBuilder += '# Learned Behavioral Guidance\n';
    contextBuilder += 'These are patterns learned from previous sessions. Follow them unless context demands otherwise.\n\n';

    for (let i = 0; i < guidance.length; i++) {
      const g = guidance[i];
      if (!g || typeof g !== 'object') continue;

      const gType = escapeXmlTags(getString(g.type)).toUpperCase();
      const gTitle = escapeXmlTags(getString(g.title));
      const gNarrative = escapeXmlTags(getString(g.narrative));

      contextBuilder += `${i + 1}. [${gType}] ${gTitle}\n`;
      if (gNarrative !== '') {
        contextBuilder += `${gNarrative}\n`;
      }
      contextBuilder += formatFactsLine(g.facts);
      contextBuilder += '\n';
    }

    contextBuilder += '</engram-guidance>\n';
  }

  // Mark all injected observation IDs (fire-and-forget).
  // Use per-session endpoint when possible so stop hook can detect utility signals.
  const allObs = [...observations, ...guidance];
  const ids = [];
  for (const obs of allObs) {
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

(async () => {
  await lib.RunHook('SessionStart', handleSessionStart);
})();
