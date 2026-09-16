# FR/SC-to-Task Traceability Matrix

**Candidate**: `001-engram-architectural-recovery`  
**Task source**: `tasks.md` T001–T091  
**Scope**: Pre-analysis deterministic mapping. Final Spec Kit analysis revalidates it against the
actual task lines and constitution.

**Edge semantics**: A row contains every direct FR/SC citation from a task plus any additional
semantic coverage that the plan/contracts make explicit. Direct-citation completeness is a strict
zero-missing-edge gate; additional semantic edges are deliberate coverage links, not missing task
metadata. Governance-only tasks remain in the governance table and are excluded from FR/SC rows
unless their task line explicitly cites an FR or SC.

## Functional Requirements

| Requirement | Task IDs | Release |
|---|---|---|
| FR-001 | T017, T021, T022, T024, T025, T026, T028 | AR-2 |
| FR-002 | T017, T021, T022, T023, T028 | AR-2 |
| FR-003 | T017, T018, T019, T020, T022, T023, T028, T030 | AR-2 |
| FR-004 | T008, T009, T022, T023, T080 | AR-1/AR-2/AR-7 |
| FR-005 | T016, T018, T019, T020, T022, T024, T025, T026, T027, T029, T030, T042 | AR-2/AR-3 |
| FR-006 | T004, T031, T032, T039, T040, T078 | AR-1/AR-3/AR-7 |
| FR-007 | T033, T034, T035, T036, T037, T038, T041, T043 | AR-3 |
| FR-008 | T044, T045, T046, T049, T055, T057 | AR-4 |
| FR-009 | T045, T047, T048, T054, T057 | AR-4 |
| FR-010 | T047, T048, T054, T081 | AR-4/AR-7 |
| FR-011 | T046, T047, T054, T060 | AR-4/AR-5 |
| FR-012 | T058, T059, T060, T061, T064, T066 | AR-5 |
| FR-013 | T039, T059, T061, T063, T064, T066, T076, T081 | AR-3/AR-5/AR-6/AR-7 |
| FR-014 | T062, T064, T066 | AR-5 |
| FR-015 | T067, T068, T070, T073, T076, T081 | AR-6/AR-7 |
| FR-016 | T067, T069, T070, T073, T074, T077 | AR-6 |
| FR-017 | T067, T071, T073, T077 | AR-6 |
| FR-018 | T006, T007, T050, T051, T052, T053, T072, T077 | AR-1/AR-4/AR-6 |
| FR-019 | T005, T011, T013, T029, T056, T072, T075 | AR-1/AR-2/AR-4/AR-6 |
| FR-020 | T011, T047, T056, T074 | AR-1/AR-4/AR-6 |
| FR-021 | T001, T002, T003, T004, T078, T084 | AR-1/AR-7 |
| FR-022 | T002, T003, T076, T078, T079, T081, T084 | AR-1/AR-6/AR-7 |
| FR-023 | T065, T082 | AR-5/AR-7 |
| FR-024 | T039, T040, T083 | AR-3/AR-7 |
| FR-025 | T004, T010, T021, T027, T035, T036, T038, T041, T043, T045, T055, T080, T086 | AR-1/AR-2/AR-3/AR-4/AR-7 |
| FR-026 | T007, T042, T051, T080 | AR-1/AR-3/AR-4/AR-7 |
| FR-027 | T082, T091 | Cross-cutting |
| FR-028 | T012, T013, T015, T029, T043, T057, T066, T077, T087, T090 | All releases |

## Success Criteria

| Criterion | Task IDs | Release |
|---|---|---|
| SC-001 | T016, T017, T018, T019, T020, T028, T030 | AR-2 |
| SC-002 | T008, T009, T023, T028, T030 | AR-1/AR-2 |
| SC-003 | T031, T032, T035, T036, T037, T038, T041, T043 | AR-3 |
| SC-004 | T044, T046, T047, T048, T057 | AR-4 |
| SC-005 | T005, T056, T057 | AR-1/AR-4 |
| SC-006 | T058, T061, T064, T066 | AR-5 |
| SC-007 | T059, T063, T066 | AR-5 |
| SC-008 | T067, T070, T074, T075, T077 | AR-6 |
| SC-009 | T067, T069, T073, T074, T075, T077 | AR-6 |
| SC-010 | T067, T070, T071, T074, T075, T077 | AR-6 |
| SC-011 | T050, T051, T052, T053, T056, T057, T075, T077 | AR-4/AR-6 |
| SC-012 | T061, T064, T066, T072, T077 | AR-5/AR-6 |
| SC-013 | T010, T033, T036, T041, T043, T086 | AR-1/AR-3/AR-7 |
| SC-014 | T001, T002, T003, T004, T078, T087 | AR-1/AR-7 |
| SC-015 | T078, T079, T080, T081, T084, T087 | AR-7 |
| SC-016 | T065, T082, T087 | AR-5/AR-7 |
| SC-017 | T039, T040, T083, T087 | AR-3/AR-7 |
| SC-018 | T012, T015, T043, T057, T066, T077, T086, T087, T090 | All releases |

## AR-2 Delta Owning Paths

| Task | Corrected implementation ownership |
|---|---|
| T017 | `internal/projectidentity/anchor.go` plus tracked root anchor migration. |
| T018 | `plugin/engram/hooks/lib.js`, `session-start.js`, and hook V3 vector tests. |
| T019 | OpenClaw `identity.ts`, `client.ts`, `config.ts`, `index.ts`, actual hook consumers, and TypeScript vectors. |
| T021 | GORM V3 model/migration registration/store constraints and PostgreSQL behavior tests. |
| T022–T023 | One `internal/projectidentity` application authority for descriptors, intents, outcomes, resolution, and audit-safe refusal data. |
| T024 | `proto/engram/v1/engram.proto`, derived `engram.pb.go`/`engram_grpc.pb.go`, `Makefile`, and gRPC translation including session start. |
| T025 | HTTP context/hook intake and route-level typed-refusal mapping. |
| T026 | `internal/mcp`, daemon slug/cache/module/tools/grpcpool, and `cmd/engram/wiring.go`; `cmd/engram/main.go` is not an identity owner. |
| T027–T030 | One comparison owner, real-fixture behavior matrix, compatibility receipt, independent checks, and staged dogfood. |

Direct FR/SC task-ID edges above remain unchanged; this table corrects path/owner truth proven by the AR-2 current-source reconciliation.

## Explicit Governance and Negative-Scope Tasks

| Task IDs | Obligation |
|---|---|
| T014, T030, T043, T057, T066, T077, T087 | Independent checker/verifier evidence is required by the constitution and release plan. |
| T085 | Exact-head SonarQube analysis, full finding correction, fresh exact-head scan, and `OK` Quality Gate are required before final backup/restore and tag/publication evidence. |
| T088, T089, T090 | Traceability, independent receipt references, clean-main build, installed proof, and housekeeping. |
| T091 | No operator working-surface implementation task may enter this recovery feature. |

## Coverage Result

- Functional requirements mapped: 28/28.
- Buildable success criteria mapped: 18/18.
- Implementation tasks with no FR/SC or explicit governance obligation: 0/91.
- Working-surface implementation tasks: 0.
