#!/usr/bin/env node
'use strict';

const lib = require('./lib');

const colorReset = '\u001b[0m';
const colorGreen = '\u001b[32m';
const colorYellow = '\u001b[33m';
const colorCyan = '\u001b[36m';
const colorGray = '\u001b[90m';
const colorRed = '\u001b[31m';

function useColorsSetting() {
  let useColors = !process.env.NO_COLOR && process.env.TERM !== 'dumb';

  if (process.env.ENGRAM_STATUSLINE_COLORS === 'false') {
    useColors = false;
  } else if (process.env.ENGRAM_STATUSLINE_COLORS === 'true') {
    useColors = true;
  }

  return useColors;
}

function asString(value) {
  return typeof value === 'string' ? value : '';
}

function safeNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

function colorize(text, colorCode, useColors) {
  return useColors ? `${colorCode}${text}${colorReset}` : text;
}

function getWorkerStats(project) {
  const base = `/api/stats${project ? `?project=${encodeURIComponent(project)}` : ''}`;
  return lib.requestGet(base, 100).catch(() => null);
}

let cachedEffectiveness = null;
let effectivenessCacheTime = 0;
const EFFECTIVENESS_CACHE_MS = 60000;

async function getEffectivenessLabel() {
  const now = Date.now();
  if (now - effectivenessCacheTime < EFFECTIVENESS_CACHE_MS && cachedEffectiveness !== null) {
    return cachedEffectiveness;
  }
  try {
    const dist = await lib.requestGet('/api/learning/effectiveness-distribution', 3000);
    if (!dist || typeof dist !== 'object') {
      cachedEffectiveness = '';
      effectivenessCacheTime = now;
      return '';
    }
    const tiers = dist.tiers || dist;
    const high = safeNumber(tiers.high);
    const medium = safeNumber(tiers.medium);
    const low = safeNumber(tiers.low);
    const total = high + medium + low;
    if (total === 0) {
      cachedEffectiveness = 'eff:--';
      effectivenessCacheTime = now;
      return cachedEffectiveness;
    }
    const highPct = Math.round((high / total) * 100);
    cachedEffectiveness = `eff:${highPct}%`;
    effectivenessCacheTime = now;
    return cachedEffectiveness;
  } catch {
    cachedEffectiveness = '';
    effectivenessCacheTime = now;
    return '';
  }
}

function formatOfflineColored(useColors) {
  const projectText = colorize('[engram]', colorGray, useColors);
  const dot = colorize('\u25CB', colorGray, useColors);
  return `${projectText} ${dot} offline`;
}

function formatStartingColored(useColors) {
  const projectText = colorize('[engram]', colorYellow, useColors);
  const dot = colorize('\u25CB', colorYellow, useColors);
  return `${projectText} ${dot} starting...`;
}

function formatDefault(stats, useColors, effLabel) {
  const retrieval = stats && stats.retrieval ? stats.retrieval : {};

  const served = safeNumber(retrieval.ObservationsServed);
  const injected = safeNumber(retrieval.ContextInjections);
  const searches = safeNumber(retrieval.SearchRequests);
  const projectObservations = safeNumber(stats.projectObservations);

  const parts = [];
  if (served > 0) {
    parts.push(`served:${served}`);
  }
  if (injected > 0) {
    parts.push(`injected:${injected}`);
  }
  if (searches > 0) {
    parts.push(`searches:${searches}`);
  }
  if (projectObservations > 0) {
    parts.push(`project:${projectObservations} memories`);
  }
  if (typeof effLabel === 'string' && effLabel.length > 0) {
    parts.push(effLabel);
  }

  const prefix = colorize('[engram]', colorGreen, useColors);
  const dot = colorize('\u25CF', colorGreen, useColors);

  if (parts.length === 0) {
    return `${prefix} ${dot}`;
  }

  return `${prefix} ${dot} ${parts.join(' | ')}`;
}

function formatCompact(stats, useColors) {
  const retrieval = stats && stats.retrieval ? stats.retrieval : {};

  const served = safeNumber(retrieval.ObservationsServed);
  const injected = safeNumber(retrieval.ContextInjections);
  const searches = safeNumber(retrieval.SearchRequests);

  const prefix = colorize('[e]', colorGreen, useColors);
  const dot = colorize('\u25CF', colorGreen, useColors);

  return `${prefix} ${dot} ${served}/${injected}/${searches}`;
}

function formatMinimal(stats, useColors) {
  const memories = safeNumber(stats && stats.projectObservations);
  const prefix = colorize('●', colorGreen, useColors);

  if (memories > 0) {
    return `${prefix} ${memories} memories`;
  }

  return `${prefix} engram ready`;
}

function formatStatusLine(stats, effLabel) {
  const useColors = useColorsSetting();

  if (!stats) {
    return formatOfflineColored(useColors);
  }

  if (stats.ready !== true) {
    return formatStartingColored(useColors);
  }

  const format = process.env.ENGRAM_STATUSLINE_FORMAT || 'default';
  if (format === 'compact') {
    return formatCompact(stats, useColors);
  }

  if (format === 'minimal') {
    return formatMinimal(stats, useColors);
  }

  return formatDefault(stats, useColors, effLabel);
}

function formatNotConfigured(useColors) {
  const projectText = colorize('[engram]', colorYellow, useColors);
  const dot = colorize('\u25CB', colorYellow, useColors);
  return `${projectText} ${dot} not configured — set ENGRAM_URL`;
}

async function handleStatusline(input) {
  if (!process.env.ENGRAM_URL) {
    return formatNotConfigured(useColorsSetting());
  }

  if (input === null || input === undefined) {
    return formatOfflineColored(useColorsSetting());
  }

  const workspace = input.workspace || input.Workspace || {};
  const projectDir =
    asString(workspace.ProjectDir) ||
    asString(workspace.project_dir) ||
    asString(workspace.CurrentDir) ||
    asString(workspace.current_dir) ||
    asString(input.CWD) ||
    asString(input.cwd);

  const project = projectDir ? lib.ProjectIDWithName(projectDir) : '';
  const [stats, effLabel] = await Promise.all([
    getWorkerStats(project),
    getEffectivenessLabel(),
  ]);
  return formatStatusLine(stats, effLabel);
}

(async () => {
  await lib.RunStatuslineHook(handleStatusline, () => formatOfflineColored(useColorsSetting()));
})();
