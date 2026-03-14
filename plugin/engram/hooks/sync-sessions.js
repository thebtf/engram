#!/usr/bin/env node
'use strict';

/**
 * sync-sessions.js — Catch-up sync for session transcripts.
 *
 * Scans ~/.claude/projects/ for JSONL session files, checks which are
 * already indexed on the server, and uploads any missing ones.
 *
 * Usage:
 *   node sync-sessions.js [--dry-run] [--limit N]
 *
 * Environment:
 *   ENGRAM_URL / ENGRAM_WORKER_HOST / ENGRAM_WORKER_PORT — server address
 *   ENGRAM_API_TOKEN — auth token (optional)
 */

const fs = require('fs');
const os = require('os');
const path = require('path');

const lib = require('./lib');

const MAX_SESSION_SIZE = 5 * 1024 * 1024; // 5 MB
const BATCH_SIZE = 100; // Check this many session IDs per request

function findSessionFiles() {
  const home = os.homedir();
  // Allow override via ENGRAM_SESSIONS_DIR for non-standard Claude Code installations.
  const configuredDir = process.env.ENGRAM_SESSIONS_DIR;
  const projectsDir =
    configuredDir && configuredDir.trim() !== ''
      ? path.resolve(configuredDir.replace(/^~(?=$|[/\\])/, home))
      : path.join(home, '.claude', 'projects');

  if (!fs.existsSync(projectsDir)) {
    console.error(`Projects directory not found: ${projectsDir}`);
    return [];
  }

  const sessions = [];

  function walkDir(dir) {
    let entries;
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch (err) {
      console.error(`Error reading directory ${dir}: ${err.message}`);
      return;
    }
    for (const entry of entries) {
      const fullPath = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walkDir(fullPath);
      } else if (entry.isFile() && entry.name.endsWith('.jsonl')) {
        const sessionId = path.basename(entry.name, '.jsonl');
        let stat;
        try {
          stat = fs.statSync(fullPath);
        } catch (err) {
          console.error(`Error stating file ${fullPath}: ${err.message}`);
          continue;
        }
        if (stat.size > 0) {
          sessions.push({
            id: sessionId,
            path: fullPath,
            size: stat.size,
          });
        }
      }
    }
  }

  walkDir(projectsDir);
  return sessions;
}

async function checkMissing(sessionIds) {
  try {
    const result = await lib.requestPost('/api/sessions/check', {
      session_ids: sessionIds,
    }, 15000);
    return Array.isArray(result.missing) ? result.missing : [];
  } catch (err) {
    console.error(`Failed to check sessions: ${err.message}`);
    return sessionIds; // Assume all missing on error
  }
}

async function uploadSession(session) {
  if (session.size > MAX_SESSION_SIZE) {
    console.error(`  Skipping ${session.id}: too large (${(session.size / 1024 / 1024).toFixed(1)} MB)`);
    return false;
  }

  const content = fs.readFileSync(session.path, 'utf8');
  const wsId = lib.WorkstationID();
  const endpoint = `/api/sessions/index?workstation_id=${encodeURIComponent(wsId)}&session_id=${encodeURIComponent(session.id)}`;

  const result = await lib.requestUpload(endpoint, content, 30000);
  console.log(`  Indexed ${session.id}: status=${result.status}, exchanges=${result.exchange_count || 0}`);
  return true;
}

async function main() {
  const args = process.argv.slice(2);
  const dryRun = args.includes('--dry-run');
  const limitIdx = args.indexOf('--limit');
  const limit = limitIdx >= 0 && args[limitIdx + 1] ? parseInt(args[limitIdx + 1], 10) : 0;

  console.log('Scanning for session files...');
  const allSessions = findSessionFiles();
  console.log(`Found ${allSessions.length} session files`);

  if (allSessions.length === 0) {
    return;
  }

  // Check in batches which sessions are already indexed
  const allIds = allSessions.map(s => s.id);
  const missingIds = new Set();

  for (let i = 0; i < allIds.length; i += BATCH_SIZE) {
    const batch = allIds.slice(i, i + BATCH_SIZE);
    const missing = await checkMissing(batch);
    for (const id of missing) {
      missingIds.add(id);
    }
  }

  const toUpload = allSessions.filter(s => missingIds.has(s.id));
  const alreadyIndexed = allSessions.length - toUpload.length;

  console.log(`Already indexed: ${alreadyIndexed}`);
  console.log(`Missing (to upload): ${toUpload.length}`);

  if (dryRun) {
    console.log('Dry run — no uploads performed');
    for (const s of toUpload) {
      console.log(`  Would upload: ${s.id} (${(s.size / 1024).toFixed(1)} KB)`);
    }
    return;
  }

  const uploadList = limit > 0 ? toUpload.slice(0, limit) : toUpload;
  let indexed = 0;
  let failed = 0;

  for (const session of uploadList) {
    try {
      const ok = await uploadSession(session);
      if (ok) indexed++;
    } catch (err) {
      console.error(`  Failed ${session.id}: ${err.message}`);
      failed++;
    }
  }

  console.log(`\nDone: ${indexed} indexed, ${failed} failed, ${alreadyIndexed} already indexed`);
}

main().catch(err => {
  console.error(`Fatal: ${err.message}`);
  process.exit(1);
});
