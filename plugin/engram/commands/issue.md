---
description: Triage engram issues — check assigned, verify your fixes, file new ones for tracked projects
---

# /engram:issue — Issue triage workflow

Systematic workflow for working with engram's cross-project issue tracker.
Run this when you want to check the full state of issues involving your
current project, or when you've found a new problem that should be filed.

## Decision rules (read first)

**Engram issues are for ENGRAM-TRACKED projects only.** If you find a problem
in a project that is NOT tracked by engram, use its native issue tracker
(GitHub, Linear, Jira, etc.) — do NOT file engram issues for external projects.

To check if a project is engram-tracked:
```
curl -s -H "Authorization: Bearer $ENGRAM_API_TOKEN" \
  http://$ENGRAM_HOST/api/issues/tracked-projects
```
Returns `{"projects": [...], "count": N}`. A project is tracked if it has
observations OR existing issues in engram — meaning an agent working on it
will actually see injected issues at session start.

**Role discipline (NON-NEGOTIABLE):**
- You are the TARGET agent when an issue's `target_project` matches your project.
  Your job: resolve or comment. You do NOT close issues you didn't create.
- You are the SOURCE agent when an issue's `source_project` matches your project.
  Your job: verify fixes, then close (fix works) or reopen (fix failed).
- Only the source agent (or dashboard operator) can close.
- Only resolve an issue when the fix is actually deployed — not when you think it will work.

## Workflow

Determine your current working project slug first. If the current directory
is a tracked engram project, use that slug. Otherwise the command may still
run in read-only mode, but creating issues is disabled.

### Step 1 — Live issues targeting your project (you as TARGET)

Fetch active work assigned to you:

```
issues(action="list", project="<your-project>", status="open,acknowledged,reopened", limit=20)
```

For each returned issue:
1. Read the title, body, and latest comments with `issues(action="get", id=N)`.
2. Treat it as YOUR project's inbox and a direct work order: study the situation, verify the claim, decide whether to implement, reject, clarify, or comment with blocker/progress.
3. `acknowledged` means delivered and accepted into your active backlog — not done.
4. If resolved: `issues(action="update", project="<your-project>", id=N, status="resolved", comment="Fixed in <commit/version>: <explanation>")`.
5. If the source project misunderstood the problem or requested the wrong change: comment with evidence and explain why; do not silently ignore it.
6. Do NOT close — that's the source agent's job.

Report at the end: how many were actionable, which are blocked, which you resolved.

### Step 2 — Issues YOU previously resolved (check for reopens and feedback)

Fetch issues you resolved that might have been reopened or commented on:

```
issues(action="list", project="<your-project>", status="reopened", limit=20)
```

Then separately for resolved issues you created or worked on, check if the
source agent added comments:

```
issues(action="list", source_project="<your-project>", status="resolved", limit=20)
```

For each reopened issue:
1. `issues(action="get", id=N)` — read the reopen reason carefully.
2. Understand WHY the fix failed. Do not assume it's still fixed.
3. If the reason is valid → plan another fix. If the reopener misunderstood → comment politely explaining.
4. Never re-resolve silently.

### Step 3 — Your cross-project issues (you as SOURCE)

Issues YOU filed against OTHER projects that are now resolved — you must verify:

```
issues(action="list", source_project="<your-project>", status="resolved", limit=20)
```

For each resolved issue you created:
1. `issues(action="get", id=N)` — read the resolution comment.
2. Treat this as YOUR follow-up inbox for cross-project dialogue: understand what the other project claims to have fixed or added, inspect comments/reports, and judge the quality of the result.
3. **Actually verify the fix works.** Test the behavior, read the commit, check the deployed version.
4. If the fix works and you're satisfied → `issues(action="close", project="<your-project>", id=N)`. This is terminal — the issue disappears from all injections.
5. If the fix does NOT work, is incomplete, or the other project misunderstood the request → `issues(action="reopen", project="<your-project>", id=N, body="Tested X, still broken because Y")` with concrete evidence.
6. If more discussion is needed before reopen/close → comment with precise feedback and what still needs verification.
7. If you cannot verify (insufficient access, needs deploy) → comment with what's missing; do NOT close.

### Step 4 — New problems discovered during this session

If during this session you encountered a bug, missing feature, or friction
in a project OTHER than your current one:

1. **Check if the other project is engram-tracked.** Fetch `GET /api/issues/tracked-projects` and look for it.
   - **Tracked:** proceed to step 4.2 (file engram issue).
   - **Not tracked:** file a GitHub/Linear issue using that project's normal workflow. Do NOT file in engram — the target agents won't see it.

2. **Check for duplicates BEFORE creating** — another agent may have already filed this:
   ```
   issues(action="list", project="<target-project>", status="open,acknowledged,reopened,resolved", limit=50)
   ```
   Read the titles. If a similar issue already exists, add a comment to that issue instead of creating a duplicate.

3. **Create a clear issue:**
   ```
   issues(action="create",
          project="<your-project>",
          target_project="<other-project>",
          title="<short imperative summary>",
          body="<evidence: what you did, what you expected, what happened, logs/commits/files>",
          priority="<critical|high|medium|low>",
          labels=["bug", ...])
   ```

   Body should include:
   - Reproduction steps (or the exact action that triggered it)
   - Expected vs actual behavior
   - Relevant file paths, commits, error messages
   - Any workaround you applied on your side

### Step 5 — Report

At the end of the workflow, give the user a concise summary:

```
Engram issues triage for <your-project>:
- Active (target): N issues, M resolved this session
- Reopened (target): K issues needing re-fix
- Resolved by others (source): P issues verified and closed, Q reopened
- New issues filed: R (for tracked projects)
- External problems found: S (filed in GitHub/etc., listed below)
```

## Anti-patterns (DO NOT)

- Do NOT file engram issues for untracked projects — use their native tracker.
- Do NOT close issues you didn't create. That's the source agent or operator.
- Do NOT create duplicates — check existing issues first with `list`.
- Do NOT resolve an issue while the fix is only half-done — comment with progress instead.
- Do NOT decide unilaterally "my part is done, splitting into new issues for X" — comment your analysis and ask the operator.
- Do NOT silently accept a reopen — understand the reason and either fix properly or explain why you think the original resolution was correct.
