#!/usr/bin/env node
'use strict';

const lib = require('./lib');

function getString(value) {
  return typeof value === 'string' ? value : '';
}

function formatFactsLine(items) {
  if (!Array.isArray(items) || items.length === 0) return '';

  let out = 'Key facts:\n';
  for (const fact of items) {
    if (typeof fact === 'string' && fact !== '') {
      out += `- ${fact}\n`;
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

  let result = {};
  try {
    result = await lib.requestGet(
      `/api/context/inject?project=${encodeURIComponent(project)}&cwd=${encodeURIComponent(cwd)}`
    );
  } catch (error) {
    console.error(`[engram] Warning: context fetch failed: ${error.message}`);
    return '';
  }

  const observations = Array.isArray(result.observations) ? result.observations : [];
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

    const obsType = getString(observation.type);
    const title = getString(observation.title);
    const typeLabel = obsType.toUpperCase();

    if (i < fullCount) {
      const narrative = getString(observation.narrative);
      contextBuilder += `## ${i + 1}. [${typeLabel}] ${title}\n`;
      if (narrative !== '') {
        contextBuilder += `${narrative}\n`;
      }
      contextBuilder += formatFactsLine(observation.facts);
      contextBuilder += '\n';
    } else {
      const subtitle = getString(observation.subtitle);
      if (subtitle !== '') {
        contextBuilder += `- [${typeLabel}] ${title}: ${subtitle}\n`;
      } else {
        contextBuilder += `- [${typeLabel}] ${title}\n`;
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

      const gType = getString(g.type).toUpperCase();
      const gTitle = getString(g.title);
      const gNarrative = getString(g.narrative);

      contextBuilder += `${i + 1}. [${gType}] ${gTitle}\n`;
      if (gNarrative !== '') {
        contextBuilder += `${gNarrative}\n`;
      }
      contextBuilder += formatFactsLine(g.facts);
      contextBuilder += '\n';
    }

    contextBuilder += '</engram-guidance>\n';
  }

  // Mark all injected observation IDs (fire-and-forget)
  const allObs = [...observations, ...guidance];
  const ids = [];
  for (const obs of allObs) {
    if (obs && typeof obs === 'object' && typeof obs.id === 'number' && obs.id > 0) {
      ids.push(obs.id);
    }
  }
  if (ids.length > 0) {
    lib.requestPost('/api/observations/mark-injected', { ids }, 3000).catch((err) => {
      console.error(`[engram] mark-injected failed: ${err.message}`);
    });
  }

  return contextBuilder;
}

(async () => {
  await lib.RunHook('SessionStart', handleSessionStart);
})();
