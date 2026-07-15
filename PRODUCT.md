# Product

## Scope

This file carries product intent for the **operator console** surface of engram.
Whole-product operational truth lives in `AGENTS.md`, architectural decisions in
`docs/DECISIONS.md`, and delivery state in the private agent-state repo.

## Register

product

## Users

Engram serves coding-agent workstations, but the operator console is used by one
technical operator who owns deployment, continuity, security, and memory quality.
The operator often keeps the console open on a secondary display, may arrive at a
page without recent project context, and must be able to distinguish a healthy
system from a plausible-looking but incomplete one quickly.

## Product Purpose

Engram preserves useful state across agent sessions and workstations: memories,
behavioral rules, issues, documents, credentials, and operational state. The
operator console makes that system understandable and controllable without
requiring database access, raw endpoint knowledge, or manual reconstruction of
derived structures.

Success means that the operator can answer three questions from the product
itself: what Engram currently knows, why it believes or exposes that state, and
what safe action will improve or recover it. A control is not complete until its
result is confirmed by the server and can be read back after the persistence
boundary implied by the action.

## Brand Personality

Honest, precise, calm. Engram should feel like a trustworthy instrument panel:
dense when the task requires density, explicit about uncertainty, and quiet
enough that real warnings carry weight.

## Anti-references

- An API explorer presented as an operator workflow.
- A database editor that asks the operator to manufacture system-derived data.
- A dashboard of decorative metrics, fake trends, or optimistic status badges.
- A science-fiction graph toy with motion but no answerable operator question.
- A UI that assumes internal IDs, environment variables, handler names, or
  architecture history are normal task language.
- Documentation that preserves removed transports or dormant scaffolds because
  their names still exist in the repository.

## Design Principles

1. **Outcome before implementation evidence.** Lead with the operator's task,
   state, next action, impact, and recovery. Endpoints and flags remain available
   as secondary evidence.
2. **The system produces; the operator supervises.** Engram derives indexes,
   relationships, classifications, and telemetry from verified domain events.
   Human mutation is reserved for approval, correction, and exceptional repair.
3. **Trust is inspectable.** Important state exposes provenance, freshness,
   scope, and the reason it exists. Empty and partial states explain their cause.
4. **Progressive power.** The default path is understandable without product
   lore; expert controls are available through deliberate disclosure and do not
   compete with the primary workflow.
5. **Persistence is part of UX.** Success requires server confirmation and
   readback. Failure preserves prior state and offers a concrete recovery path.

## Accessibility & Inclusion

The console targets WCAG 2.2 AA. Primary journeys must work with keyboard only,
visible focus, semantic names and state announcements, reduced motion, 200%
zoom, and non-color status cues. Touch targets are at least 44 by 44 CSS pixels
on coarse-pointer layouts. Russian and English task language must remain
complete; technical identifiers stay unchanged only where they are evidence.

## Design Authority

Strategic product intent lives in this file. The current visual authoring source
is `.od/DESIGN.md`; tracked public design contracts live under
`design/operator-console/`; deployable UI lives under `apps/operator-console/`.
Promotion is one-way under `design/operator-console/PROMOTION-CONTRACT.md`.
Visual parity is established by browser evidence, not by similar component names
or copied CSS.
