# IMAGE-REMEDIATION-R2 behavior signal

- Signal: immutable-image-release-contract success rate.
- Protected user outcome: an operator can deploy and roll back the exact scanned server, operator-console, and PostgreSQL images without a stale branch, moved tag, hostile ref, manual dispatch, or pre-existing registry tag replacing the intended release identity.
- Measurement window: every image workflow run and every production release.
- Target: 100% of main/manual runs are write-free; 100% of release runs either publish the six exact canonical destinations or fail before the first write; zero HIGH/CRITICAL findings; all runtime proof flags pass; zero owned resource residue.
- Method: permanent critical workflow/model fixtures, exact-IID no-cache build/scan/runtime gate, final image-set manifest, GitHub ruleset readback, and registry compare-before-write plan.
- Evidence source: production-readiness master plan invariant 15 and IMAGE-REMEDIATION-R2 row.
- Classification: BEHAVIOR_VERIFIED once the final exact-IID matrix and adversarial publication model are GREEN.
