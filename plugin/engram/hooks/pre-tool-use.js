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

function extractToolInput(input) {
  return input.tool_input && typeof input.tool_input === 'object'
    ? input.tool_input
    : {};
}

function extractFilePath(toolInput) {
  for (const key of ['file_path', 'path', 'filePath', 'file', 'filename']) {
    const value = getString(toolInput[key]);
    if (value !== '') return value;
  }
  return '';
}

function shouldSkipPath(filePath) {
  const normalizedPath = getString(filePath).toLowerCase();
  if (normalizedPath === '') return false;
  const unixTempMarker = '/tmp/';
  const windowsTempPattern = /[\\/]temp[\\/]/;
  const dependencyDir = 'node_modules';
  return normalizedPath.includes(unixTempMarker)
    || windowsTempPattern.test(normalizedPath)
    || normalizedPath.includes(dependencyDir);
}

function classifyObservations(observations) {
  const warnings = [];
  const contextObs = [];
  const warningTypes = { bugfix: true, pitfall: true };
  const warningConcepts = { 'anti-pattern': true, gotcha: true, 'error-handling': true, security: true };

  for (const obs of observations) {
    if (!obs || typeof obs !== 'object') continue;
    const obsType = getString(obs.type).toLowerCase();
    const concepts = Array.isArray(obs.concepts) ? obs.concepts : [];
    const isWarning = warningTypes[obsType] || concepts.some((c) => warningConcepts[c]);
    if (isWarning) warnings.push(obs);
    else contextObs.push(obs);
  }

  return { warnings, contextObs };
}

function classifyMatches(matches) {
  const warnings = [];
  const contextObs = [];
  for (const match of matches) {
    if (!match || typeof match !== 'object') continue;
    if (getString(match.kind).toLowerCase() === 'warning') warnings.push(match);
    else contextObs.push(match);
  }
  return { warnings, contextObs };
}

function renderEntries(entries, kindLabel, filePath, mapper) {
  if (!Array.isArray(entries) || entries.length === 0) return '';
  let out = `\n## ${kindLabel} (${entries.length})\n\n`;
  for (const entry of entries) {
    const mapped = mapper(entry, filePath);
    out += `### [${mapped.type}] ${mapped.title}\n`;
    if (mapped.narrative) out += `${mapped.narrative}\n`;
    for (const fact of mapped.facts) {
      if (typeof fact === 'string' && fact !== '') out += `- ${escapeXmlTags(fact)}\n`;
    }
    out += '\n';
  }
  return out;
}

function renderFileContext(filePath, warnings, contextObs) {
  let context = '<file-context>\n';
  context += `# Known Context for ${escapeXmlTags(filePath)}\n`;
  context += renderEntries(warnings, 'WARNINGS', filePath, (obs) => ({
    title: escapeXmlTags(getString(obs.title)),
    type: escapeXmlTags(getString(obs.type)).toUpperCase(),
    narrative: escapeXmlTags(getString(obs.narrative)),
    facts: Array.isArray(obs.facts) ? obs.facts : [],
  }));
  context += renderEntries(contextObs, 'Context', filePath, (obs) => ({
    title: escapeXmlTags(getString(obs.title)),
    type: escapeXmlTags(getString(obs.type)).toUpperCase(),
    narrative: escapeXmlTags(getString(obs.narrative)),
    facts: Array.isArray(obs.facts) ? obs.facts : [],
  }));
  context += '</file-context>';
  return context;
}

function renderTriggerContext(toolName, filePath, warnings, contextObs) {
  const label = filePath !== '' ? filePath : toolName;
  let context = '<file-context>\n';
  context += `# Known Context for ${escapeXmlTags(label)}\n`;
  context += renderEntries(warnings, 'WARNINGS', filePath, (match) => ({
    title: `Trigger Match #${getString(String(match.observation_id || ''))}`,
    type: escapeXmlTags(getString(match.kind)).toUpperCase(),
    narrative: escapeXmlTags(getString(match.blurb)),
    facts: [],
  }));
  context += renderEntries(contextObs, 'Context', filePath, (match) => ({
    title: `Trigger Match #${getString(String(match.observation_id || ''))}`,
    type: escapeXmlTags(getString(match.kind)).toUpperCase(),
    narrative: escapeXmlTags(getString(match.blurb)),
    facts: [],
  }));
  context += '</file-context>';
  return context;
}

async function fetchByFileContext(project, filePath) {
  const params = new URLSearchParams({ path: filePath, limit: '10' });
  if (project) params.set('project', project);
  const result = await lib.requestGet(`/api/context/by-file?${params.toString()}`, 200);
  const observations = Array.isArray(result.observations) ? result.observations : [];
  return classifyObservations(observations);
}

async function fetchTriggerContext(project, sessionID, toolName, toolInput) {
  const normalizedInput = { ...toolInput };
  if (toolName === 'Bash') {
    const command = getString(toolInput.command || toolInput.cmd);
    if (command) normalizedInput.command = command;
  }
  const filePath = extractFilePath(toolInput);
  if (filePath) normalizedInput.file_path = filePath;
  const result = await lib.requestPost('/api/memory/triggers', {
    tool: toolName,
    params: normalizedInput,
    project,
    session_id: sessionID,
  }, 200);
  const matches = Array.isArray(result.matches) ? result.matches : Array.isArray(result) ? result : [];
  return classifyMatches(matches);
}

async function handlePreToolUse(ctx, input) {
  const toolName = getString(input.tool_name);
  const toolInput = extractToolInput(input);
  const project = getString(ctx.Project);
  const sessionID = getString(ctx.SessionID);

  if (toolName === 'Edit' || toolName === 'Write') {
    const filePath = extractFilePath(toolInput);
    if (!filePath || shouldSkipPath(filePath)) return '';
    if (sessionID) lib.appendSessionFile(sessionID, filePath);

    try {
      const [fileResult, triggerResult] = await Promise.all([
        fetchByFileContext(project, filePath),
        fetchTriggerContext(project, sessionID, toolName, toolInput),
      ]);
      const warnings = [...fileResult.warnings, ...triggerResult.warnings];
      const contextObs = [...fileResult.contextObs, ...triggerResult.contextObs];
      if (warnings.length === 0 && contextObs.length === 0) return '';
      const context = renderFileContext(filePath, warnings, contextObs);
      console.error(`[pre-tool-use] Injecting ${warnings.length} warnings + ${contextObs.length} context for ${filePath}`);
      return JSON.stringify({ systemMessage: context });
    } catch (error) {
      console.error(`[pre-tool-use] Context query failed: ${error.message}`);
      return '';
    }
  }

  if (toolName === 'Bash' || toolName === 'Read') {
    try {
      const filePath = extractFilePath(toolInput);
      const triggerInput = toolName === 'Read' && filePath
        ? (() => {
            const readCounts = (toolInput.read_counts && typeof toolInput.read_counts === 'object')
              ? { ...toolInput.read_counts }
              : {};
            if (readCounts[filePath] === undefined) {
              readCounts[filePath] = 3;
            }
            return { ...toolInput, read_counts: readCounts };
          })()
        : toolInput;
      const triggerResult = await fetchTriggerContext(project, sessionID, toolName, triggerInput);
      if (triggerResult.warnings.length === 0 && triggerResult.contextObs.length === 0) return '';
      const context = renderTriggerContext(toolName, filePath, triggerResult.warnings, triggerResult.contextObs);
      console.error(`[pre-tool-use] Injecting ${triggerResult.warnings.length} warnings + ${triggerResult.contextObs.length} context for ${toolName}`);
      return JSON.stringify({ systemMessage: context });
    } catch (error) {
      console.error(`[pre-tool-use] Trigger query failed: ${error.message}`);
      return '';
    }
  }

  return '';
}

(async () => {
  await lib.RunHook('PreToolUse', handlePreToolUse);
})();

module.exports = {
  handlePreToolUse,
};
