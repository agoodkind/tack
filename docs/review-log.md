# Review log

One row per pre-merge review: date, branch, class, reviewer tier, verdict,
catches (blocker/should-fix/nit), escapes, notes.

| Date | Branch | Class | Tier | Verdict | B/SF/N | Escapes | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-08-23 | tack-458-audit-host-commands (fe7585e+dab9845) | security/operator-cli | strongest-model adversarial | MERGE-READY | 1/0/2 | none | Blocker (uncapped, unresumable audit query) fixed as dab9845 and re-verified; RedactPIIRefs mutation caught by the DSN-gated test. |
| 2026-08-23 | tack-457-membership-gate (42f2673+fbbf852, re-verified as 426dca7+2d5c89c) | security/authz | strongest-model adversarial | MERGE-READY | 0/2/2 | TACK-459 (chain-append concurrency test crashes single-node throwaway YB; pre-exists on main) | Both should-fixes (index-trusting search, foreign relationship endpoint render) fixed as 2d5c89c; both membership-gate mutations caught red/green. |
| 2026-08-28 | tack-461-zero-org-backfill (9e7c570+41c8ad3+7effe9f, re-verified as +910729c) | security/ledger-rewrite | strongest-model adversarial | MERGE-READY | 1/1/3 | row.go Row doc comment named the removed MCP tool surface (TACK-456); fixed in 910729c | Blocker reproduced on the throwaway DB (deriveBackfillTarget refused the system org production's operator rows carry, so the command refused its target deployment) and fixed as 910729c with the derivation test; exemption red/green re-verified against the new test; six mutations red/green total; moved v1-shape nil-org row proven to verify as v3. |
