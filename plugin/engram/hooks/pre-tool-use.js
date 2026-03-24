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

async function handlePreToolUse(ctx, input) {
  // Only intercept Edit and Write tools — file-modifying operations (FR-3)
  const toolName = getString(input.tool_name);
  if (toolName !== 'Edit' && toolName !== 'Write') {
    return '';
  }

  // Extract file path from tool input
  const toolInput = input.tool_input && typeof input.tool_input === 'object'
    ? input.tool_input
    : {};
  const filePath = getString(toolInput.file_path);
  if (!filePath) {
    return '';
  }

  // Skip non-project paths (e.g., temp files, system paths)
  if (filePath.includes('/tmp/') || filePath.includes('\\Temp\\') || filePath.includes('node_modules')) {
    return '';
  }

  const project = getString(ctx.Project);

  // Query engram for file-specific observations (200ms timeout — NFR-3)
  let observations = [];
  try {
    const params = new URLSearchParams({ path: filePath, limit: '10' });
    if (project) params.set('project', project);
    const result = await lib.requestGet(`/api/context/by-file?${params.toString()}`, 200);
    observations = Array.isArray(result.observations) ? result.observations : [];
  } catch (error) {
    // Graceful degradation: return empty, don't block (NFR-3)
    console.error(`[pre-tool-use] File context query failed: ${error.message}`);
    return '';
  }

  if (observations.length === 0) {
    return '';
  }

  // Build <file-context> block for systemMessage injection
  let context = '<file-context>\n';
  context += `# Known Context for ${escapeXmlTags(filePath)}\n`;
  context += `Found ${observations.length} relevant observation(s) about this file.\n\n`;

  for (const obs of observations) {
    if (!obs || typeof obs !== 'object') continue;
    const title = escapeXmlTags(getString(obs.title));
    const obsType = escapeXmlTags(getString(obs.type)).toUpperCase();
    const narrative = escapeXmlTags(getString(obs.narrative));

    context += `## [${obsType}] ${title}\n`;
    if (narrative) {
      context += `${narrative}\n`;
    }

    const facts = Array.isArray(obs.facts) ? obs.facts : [];
    if (facts.length > 0) {
      context += 'Key facts:\n';
      for (const fact of facts) {
        if (typeof fact === 'string' && fact !== '') {
          context += `- ${escapeXmlTags(fact)}\n`;
        }
      }
    }
    context += '\n';
  }

  context += '</file-context>';

  console.error(`[pre-tool-use] Injecting ${observations.length} file-context observations for ${filePath}`);

  // Return systemMessage — no decision field needed (approve by default)
  return JSON.stringify({ systemMessage: context });
}

(async () => {
  await lib.RunHook('PreToolUse', handlePreToolUse);
})();
