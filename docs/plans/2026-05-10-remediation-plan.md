# Plan: backup unblock → validate → env + docker remediation

## Sequence

1. Docker SDK migration for the backup path only (assertDockerContext + 4 runDockerCmd in backup files). Unblocks backup.
2. Validate backup: `make backup`, `make backup-verify`, tampered-verify regression, `make backup-restore-test`.
3. Env remediation in full (env-var-remediation-plan.md).
4. Docker SDK migration for the deploy path (deploy_up.go compose-up + remaining sites).

## Step 1 scope (4 sites, in-container path only)

| Site | Replace with |
|---|---|
| `backup_run.go:44` assertDockerContext | Delete call. SDK uses `client.FromEnv` → bind-mounted socket. |
| `backup_restore.go:48` assertDockerContext | Same. |
| `backup_fdb.go:190,259,284` runDockerCmd (fdbbackup ops) | `cli.ContainerCreate` + `Start` + `Wait` + `Logs` against foundationdb image |
| `backup_meilisearch.go:28` runDockerCmd (tar volume) | Same pattern with alpine image |
| `backup_restore_meili.go:45` runDockerCmd | Same |
| `backup_restore.go:143` runDockerCmd (scratch container) | Same |

`assertDockerContext` and `runDockerCmd` functions stay in `dockerctl.go` for now (deploy.go and deploy_up.go still call them); fully deleted in step 4.

## Step 4 scope (3 sites, deploy path)

| Site | Replace with |
|---|---|
| `deploy.go:173` assertDockerContext | Delete. |
| `deploy_up.go:30` runDockerCmd (compose up) | `compose-spec/compose-go/v2` parse + `docker/compose/v2/pkg/api` drive |
| `dockerctl.go` (whole file) | Delete after callers are gone |

`compose-spec/compose-go` v2.10.x and `docker/compose/v2/pkg/api` confirmed active (Docker org, used inside Docker Compose itself). No viable shell-out-free alternative for compose semantics.

## Tickets (file + mark in_progress on exit)

- **Backup-path SDK migration** (high, in_progress) — step 1.
- **Backup validation** (high, blocks on above) — step 2.
- **Env remediation** (high, blocks on above) — step 3, tracks `docs/plans/env-var-remediation-plan.md`.
- **Deploy-path SDK migration** (high, blocks on above) — step 4.
- **envDefault silent-trap fix** (medium, blocks on env remediation) — 6 fields with `""`-silently-overridden behavior: MeiliMasterKey, BackupDockerContext, DeployDockerContext, Env, MeiliURL, TemporalAddress. Fix via remove-default OR `*string` pointer.

## envDefault silent-trap audit summary

42 envDefault declarations total. ~36 benign (numeric tunables, image/network names). 6 dangerous: see above.

## Verification per step

- After step 1: `make backup` produces `/root/backups/tack-<TS>/`, `grep -rn "exec.Command" internal/ops/backup*` returns 0.
- After step 2: backup-verify and restore-test exit 0; tampered byte fails verify.
- After step 4: `grep -rn "exec.Command\|exec.CommandContext" internal/ops/` returns only `git` and `date` invocations in deploy_build.go.

## Persistence

On exit: copy this file to `docs/plans/2026-05-10-remediation-plan.md`, commit, push.
