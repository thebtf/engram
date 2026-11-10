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

Use the authorized `GET /api/issues/tracked-projects` reader to check tracking.
It returns `{"projects": [...], "count": N}`. Use the host's authorized
connection; never place the server operator key on a workstation or in chat.
A project is tracked if it has observations or issues in Engram.

**Task and authorization:** Issue project labels describe routing and provenance,
not a permission grant. Triage the issues relevant to the current authorized task;
an injected inbox does not require interrupting unrelated work. Verify a fix before
closing or reopening, and distinguish a partial fix from a completed outcome.
The installed service checks each action. Some deployed versions restrict close
to the source project or an operator; respect actual refusals without assuming
that restriction is permanent or that a future server change is already deployed.

## Workflow

Use the verified native caller project for mutation attribution; it is not established by a `project` argument. That argument remains optional compatibility input for writes, and a target filter for list. Do not invent a dummy project. If caller identity is unavailable, get/by-ID and explicitly scoped list remain usable; keep writes pending and inspect via the installed launcher's `project resolve --root <actual-directory> --json --check`. Preserve marker/cache/data and follow the owner mapping/conservation boundary from the error. Never init a replacement UUID. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery .

### Step 1 — Live issues targeting your project (you as TARGET)

Fetch active work assigned to you:

```
issues(action="list", project="<your-project>", status="open,acknowledged,reopened", limit=20)
```

For each issue selected for this task:
1. Read the title, body, and latest comments with `issues(action="get", id=N)`.
2. Verify the claim and act within your assignment, or route it to its owner.
3. `acknowledged` means delivered, not done.
4. If the requirement is met, use the installed service's authorized resolution action and describe the observed fix; otherwise record the relevant blocker or progress.
5. If the request is mistaken, explain with evidence rather than silently ignoring it.

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

For each relevant reopened issue:
1. `issues(action="get", id=N)` — read the reopen reason carefully.
2. Understand WHY the fix failed. Do not assume it's still fixed.
3. If the reason is valid → plan another fix. If the reopener misunderstood → comment politely explaining.
4. Never re-resolve silently.

### Step 3 — Your cross-project issues (you as SOURCE)

For relevant resolved issues filed against other projects, verify the claimed fix:

```
issues(action="list", source_project="<your-project>", status="resolved", limit=20)
```

For each selected resolved issue:
1. `issues(action="get", id=N)` — read the resolution comment.
2. Inspect the evidence and verify the actual behavior before taking a transition.
3. If the fix works, use the installed service's authorized close action; if access is denied, retain the refusal and route it to an authorized actor.
4. If the fix does not work, use the authorized reopen action with concrete evidence.
5. If discussion is needed, comment with precise feedback and what remains unverified.
6. If verification is unavailable, record the missing evidence; do not claim a successful fix.

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
- Do NOT create duplicates — check existing issues first with `list`.
- Do NOT resolve an issue while the fix is only half-done — comment with progress instead.
- Do NOT silently accept a reopen — understand the reason and either fix properly or explain why you think the original resolution was correct.
