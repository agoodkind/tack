# FDB production backup report

**Verdict:** valid backup created.

`fdbbackup describe` reports `Restorable: true` against the produced backup, so this is the first verified-recoverable artifact for the production cluster (the existing `/root/backups/` script outputs were targeting the wrong volume and are not used here).

## Locations

- Snapshot directory on CT 117: `/root/fdb-snapshots/snapshot-20260509T051802Z/`
- Bundled tarball on CT 117: `/root/fdb-snapshots/snapshot-20260509T051802Z.tar.gz` (3,460,260 bytes)
- Local offsite copy: `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/fdb-snapshot-20260509T051802Z.tar.gz`

## Anonymous volume identified

`docker inspect tack-fdb-1` shows the anonymous volume mounted at `/var/fdb/data`:

- Volume ID: `7a90eb88d56cb658fb9922b5268196a9361ed389d0a6d9b15c683d33781755b8`
- Short ID used in tarball name: `7a90eb88d56c`
- Host path: `/var/lib/docker/volumes/7a90eb88d56cb658fb9922b5268196a9361ed389d0a6d9b15c683d33781755b8/_data`
- Live size at backup time: 16M (8 MB key-value, 210 MB disk total per `fdbcli status`)

The named `tack_fdb-data` volume is mounted at `/var/fdb` and only contains `lib/`, logs, and an empty `data/` (the anonymous volume is shadowing it). The existing `/root/backups/` script tars the named volume, which is why its outputs are empty as far as the database is concerned.

## SHA-256 manifest (remote, /root/fdb-snapshots/snapshot-20260509T051802Z/MANIFEST.sha256)

```
2079c37f0b6193e50987226839d6a3ccdb3ab7244506a2314312e42048bc0229  ./anonymous-volume-7a90eb88d56c.tar.gz
5071e27f52a9545ccf17505c82d749087cb64a7642511f55c1060192f2d8b1da  ./fdbbackup/backup-2026-05-09-05-31-51.592652/kvranges/snapshot.000001168758944639/0/range,1168758996585,6cb60f892376d29bd8bb48fadd9e63fd,1048576
ed7ce33d72c5e0a6bcb854b48695f124d6f81104909d0325f74c3f3203a6b3cd  ./fdbbackup/backup-2026-05-09-05-31-51.592652/kvranges/snapshot.000001168758944639/0/range,1168759002403,05d586baa4d170975f56e5d90fd9e126,1048576
a9501831ec9daa239ad8ee8e08d0be9f974ed73929bf8e4dd01c6e6568950d62  ./fdbbackup/backup-2026-05-09-05-31-51.592652/kvranges/snapshot.000001168758944639/0/range,1168759002403,92f80013e1015cfe31e51a5e55b6a70a,1048576
c17148d788c851327437931d755046464f3a69e8db6b778fa6f1ceb137439106  ./fdbbackup/backup-2026-05-09-05-31-51.592652/kvranges/snapshot.000001168758944639/0/range,1168759018422,c3c7a9142399d1952f48a1ffb83b09b9,1048576
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  ./fdbbackup/backup-2026-05-09-05-31-51.592652/logs/0000/0011/log,1168758944639,1168778944639,013147cb7a5f5d51d0e38aba9515abdb,1048576
5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9  ./fdbbackup/backup-2026-05-09-05-31-51.592652/properties/file_level_encryption
0bc13c392d7f7acd3dcc2a134b1c291128f103934132e83d3bd48f46e88170de  ./fdbbackup/backup-2026-05-09-05-31-51.592652/properties/log_begin_version
eb362d89af70aa3ab97a3cd35869fbbac661eb9e0c301dfac326af4c376ce5a8  ./fdbbackup/backup-2026-05-09-05-31-51.592652/properties/log_end_version
5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9  ./fdbbackup/backup-2026-05-09-05-31-51.592652/properties/mutation_log_type
5dfd9f29228dd300bd17458f640153051315a6418892d403e02352dda52fd4d1  ./fdbbackup/backup-2026-05-09-05-31-51.592652/snapshots/snapshot,1168758996585,1168759018422,4627113
```

The 0-byte mutation log (SHA `e3b0c44298fc1c149...`, the SHA-256 of empty string) is expected: production write-rate was `0 Hz` during the backup window, so the mutation log range covers no writes. `fdbbackup describe` still reports the backup as restorable end-to-end.

## SHA-256 of the bundled tarball (remote and local)

Both endpoints computed the same digest, so the offsite copy is bit-identical to the source:

```
015307c93d233cf617d0a3e9fd596cfc0d7ad487ec02f4033d528874442eb85b  snapshot-20260509T051802Z.tar.gz   (CT 117)
015307c93d233cf617d0a3e9fd596cfc0d7ad487ec02f4033d528874442eb85b  fdb-snapshot-20260509T051802Z.tar.gz (local)
```

## fdbbackup status (raw)

```
The previous backup on tag `default' at file:///snapshot/fdbbackup/backup-2026-05-09-05-31-51.592652 completed at version 1168778944638.
BackupUID: dbf7bdf80aaafeb990ca9285ba350ce5
BackupURL: file:///snapshot/fdbbackup/backup-2026-05-09-05-31-51.592652
```

`fdbbackup describe` (parseability check):

```
URL: file:///snapshot/fdbbackup/backup-2026-05-09-05-31-51.592652
Restorable: true
Partitioned logs: false
File-level encryption: false
Snapshot:  startVersion=1168758996585 endVersion=1168759018422 totalBytes=4627113 restorable=true expiredPct=0.00
SnapshotBytes: 4627113
MinLogBeginVersion:      1168758944639
ContiguousLogEndVersion: 1168778944639
MaxLogEndVersion:        1168778944639
MinRestorableVersion:    1168759018422
MaxRestorableVersion:    1168778944638
```

## Cluster health

- Pre-backup: `fdbcli status` reported `Replication health - Healthy`, fault tolerance 0 (single-process cluster), 8 MB key-value, 210 MB disk used, 73.2 GB free.
- Post-backup: same numbers; `Running backups - 0`, `Replication health - Healthy`. No restarts, no recreates, no config edits.

## Caveats

- The anonymous-volume tar (`anonymous-volume-7a90eb88d56c.tar.gz`, 2.6 MB compressed) was taken against a live FDB data directory. It carries the same consistency caveat as the existing `/root/backups/` script: the SQLite files and `.fdq` queues may include partial writes. It is a belt-and-suspenders artifact only. The `fdbbackup` output is the authoritative recovery resource here.
- The cluster runs in `single` redundancy mode with one process; fault tolerance is 0 by design. That has nothing to do with this backup, but it means there is no replica fallback if the primary disk fails before the next backup.
- `fdbbackup` requires a `backup_agent` to be running for the snapshot to actually move data. The first attempt of this run looked stalled because no agent was active. The fix was to start a `backup_agent` sidecar (image `foundationdb/foundationdb:7.4.6`, mounting `/etc/foundationdb` and the snapshot dir, attached to `tack_default`) before issuing `fdbbackup start -w`. The agent was stopped and removed after the snapshot completed; nothing was left running.

## Recommended next step

The operator now has a verified-restorable artifact and can safely proceed to remediation mutations. Two follow-ups worth scheduling separately, in priority order:

1. Replace `/root/backups/` script's volume target with the anonymous volume ID (or, better, switch it to running `fdbbackup` against a dedicated long-running `backup_agent`). The existing script will keep silently producing empty snapshots until that is fixed.
2. Consider running a persistent `backup_agent` Compose service so future `fdbbackup start` invocations do not need a manual sidecar bring-up; this also enables continuous mutation-log backup so point-in-time restore becomes possible.
