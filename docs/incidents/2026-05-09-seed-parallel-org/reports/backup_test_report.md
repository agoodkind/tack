# FDB backup restore validation: tack-20260509T042729Z

## 1. Verdict

**Not viable.** The backup tarball `tack_fdb-data.tar.gz` does not contain any FoundationDB data files. Restoring from this backup would yield an empty database.

## 2. Evidence summary

- The `foundationdb/foundationdb:7.4.6` image declares `VOLUME /var/fdb/data` in its Dockerfile, so Docker creates an anonymous volume that shadows that path.
- `docker inspect tack-fdb-1` shows two mounts: the named volume `tack_fdb-data` at `/var/fdb`, and an anonymous volume `7a90eb88d56c...` at `/var/fdb/data`.
- All real FDB data files (`storage-*.sqlite`, `log2-*.sqlite`, `*.fdq`, `clusterId`, `serverCheckpoints/`) live in the anonymous volume at `/var/lib/docker/volumes/7a90eb88d56c.../`, totaling ~211 MB.
- The backup script (`scripts/backup.sh` line 16-20) only tars `tack_fdb-data` and `tack_fdb-cluster`. It never touches the anonymous volume.
- Verified by `tar -tzf tack_fdb-data.tar.gz`: 66 entries, all in `./lib`, `./logs`, `./scripts`, plus `./fdb.cluster` and `./version`. The `./data/` directory is present but empty.
- Extracting the tarball into a scratch volume produced `data/` with size 4 KiB (empty dir) versus production `/var/fdb/data` size ~211 MiB.
- Booting a scratch FDB container against the extracted volume produced a brand-new cluster on a fresh empty data dir; `fdbcli status` timed out then bootstrapped, confirming no recoverable state was carried in.
- The same defect applies to every prior backup created by this script while the `7a90eb88d56c...` anonymous volume has existed (since 2026-04-25 per its `CreatedAt`).

## 3. Backup contents inventory

Source: `/root/backups/tack-20260509T042729Z/` on CT 117.

| File | Size | Notes |
|---|---|---|
| `tack_fdb-data.tar.gz` | 85,763,274 B | 66 entries, mostly trace XML logs and `lib/`. Empty `./data/`. |
| `tack_fdb-cluster.tar.gz` | 87 B | Empty volume; `tack_fdb-cluster` is unused in the compose spec. |
| `tack_meili-data.tar.gz` | 13,152,117 B | Not validated in this test. |
| `yugabyte-live.tar.gz` | 513,501,161 B | In-container tar of `/home/yugabyte/var`. Not validated here. |
| `users.csv` | 192 B | Logical CSV. |
| `api_tokens.csv` | 762 B | Logical CSV. Tokens are SHA-256 hashes; not redacted further here. |
| `org_members.csv` | 178 B | Logical CSV. |

Total backup directory weight: ~585 MB. Free disk on CT 117: 73 GiB, so space was not a constraint.

## 4. FDB scratch cluster status output (raw)

`fdbcli --exec status` against `fdb-restore-test`:

```
WARNING: Long delay (Ctrl-C to interrupt)
Using cluster file `/var/fdb/fdb.cluster'.

Timed out fetching cluster status.

Configuration:
  Redundancy mode        - unknown
  Storage engine         - unknown
  Log engine             - unknown
  Coordinators           - unknown

Cluster:
  FoundationDB processes - unknown
  Machines               - unknown
```

Container log: `Starting FDB server on 172.18.0.2:4500 / FDBD joined cluster.` The FDB image entrypoint rewrote the backup's `fdb.cluster` (which points at the production GUA `3d06:bad:b01:0:7ac::3:4500`) to its own container IP and started a fresh cluster against an empty data volume. The cluster was unconfigured (no `configure new`) so status fetch timed out.

## 5. Data verification results

Not performed. Step 6 of the runbook (look for org `019dc5ad-0408-7e43-9c4d-d3e6736ac058`, workspace `019dc5ad-0469-71e0-9e73-711bbcc0e93d`, the seven projects, and confirm absence of org `3dc1c593...` and workspace `351ebbfa...`) requires a working cluster with data. The data dir was empty, so there was nothing to read. The scratch cluster was unconfigured and would have returned no key-value data even after `configure new`.

## 6. Caveats and risks

- Yugabyte and Meilisearch tarballs were not tested. They likely have the same anonymous-volume problem if their respective images declare nested `VOLUME` paths; worth a follow-up check before relying on either.
- The Yugabyte tarball uses an in-container `tar` of `/home/yugabyte/var` (compose script line 27), which sidesteps the Docker-volume layering issue but captures live writes; consistency is best-effort.
- The CSV dumps for `users`, `api_tokens`, `org_members` look intact and would let auth survive a rebuild, but every product entity in FDB would be gone.
- The `tack_fdb-cluster` named volume in compose is effectively dead weight; the cluster file lives at host bind mount `/etc/foundationdb` per the app service's `volumes:` block. Worth pruning from compose and the backup loop.
- All prior backups produced by `scripts/backup.sh` since 2026-04-25 share this defect.

## 7. Recommended next action

**Do not restore from this backup.** Treat `tack-20260509T042729Z/tack_fdb-data.tar.gz` as a non-recovery artifact. Production FDB is currently healthy (`fdbcli status minimal` → "The database is available"), so recovery is not urgent, but the backup story needs to change before the next incident.

Concrete options to investigate, in order of preference:

1. Switch FDB backups to `fdbbackup` (logical, online, consistent) writing into a host-bind path, then tar that path. Native, supported, no Docker-volume layering involved.
2. If sticking with volume-tar, tar the anonymous volume directly by ID (e.g. `docker inspect tack-fdb-1` → mount source for `/var/fdb/data`) or stop FDB briefly and tar the host path under `/var/lib/docker/volumes/<anon-id>/_data`.
3. Override the anonymous `VOLUME` by binding the named `tack_fdb-data` to `/var/fdb/data` directly in compose (or running an FDB image without the `VOLUME` declaration), so the named volume actually contains the data files.
4. Test every new backup format by booting a scratch cluster and running `fdbcli configure new single ssd` then a known read.

## 8. Cleanup confirmation

| Resource | Created | Destroyed | Verified |
|---|---|---|---|
| Docker network `fdb-restore-test-net` | yes | `docker network rm` | absent in `docker network ls --filter name=restore-test` |
| Docker volume `fdb-data-restore-test` | yes | `docker volume rm` | absent in `docker volume ls --filter name=restore-test` |
| Docker volume `fdb-cluster-restore-test` | yes | `docker volume rm` | absent in `docker volume ls --filter name=restore-test` |
| Docker container `fdb-restore-test` | yes | `docker stop && docker rm` | absent in `docker ps -a --filter name=restore-test` |

Production volumes (`tack_fdb-data`, `tack_fdb-cluster`) and production containers (`tack-fdb-1`, `tack-app-1`, `tack-yugabyte-1`, `tack-meili-1`) were not touched. Production FDB confirmed healthy after the test via `docker exec tack-fdb-1 fdbcli --exec "status minimal"` returning "The database is available."
