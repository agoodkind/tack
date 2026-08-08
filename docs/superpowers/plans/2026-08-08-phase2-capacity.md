# Phase 2 capacity reading and sizing decision

Read from both hypervisors on 2026-08-08 before any guest was declared. Every
number below came from `free -g`, `pvesm status`, and per-guest `pct config`
on the hypervisor itself.

## Measured

| Hypervisor | Physical RAM | Configured across guests | Datastore free |
|---|---|---|---|
| vault | 94 GB | 58.8 GB (60160 MB across 9 guests) | 390 GB on local-lvm |
| suburban | 31 GB | 13.4 GB (13696 MB across 7 guests) | 1140 GB on local-zfs |

Guest 117 (tack) alone holds 24576 MB of vault's committed memory, because it
runs every data service today. Guest 217 (tack-qa) holds 8192 MB for the same
reason. Both shrink to app-guest size once phases 3 through 5 move the data
services onto the data guests.

## Verdict: the plan's proposed sizing does not fit either hypervisor

The plan proposed prod data guests at 16384 MB and testbed data guests at
6144 MB. Against the measurements:

- vault: proposed additions total 57344 MB. Committed would reach 115 GB
  against 94 GB physical, and the proposed 360 GB of disk would leave only
  30 GB free on local-lvm.
- suburban: proposed additions total 22528 MB against 17.6 GB of headroom.

Container memory is a ceiling rather than a reservation, so overcommit is
survivable while the new guests run nothing. It stops being survivable in
phase 3, when database nodes start on them.

## Revised sizing

Testbed (suburban), applied now:

| Guest | Cores | Memory | Disk |
|---|---|---|---|
| tack-data1/2/3 | 2 | 4096 MB | 40 GB |
| tack-app2 | 2 | 2048 MB | 30 GB |

Total added: 14336 MB against 17.6 GB of headroom, leaving about 3 GB of
slack. The testbed validates topology, replication, failover, and the export
shape. It does not validate memory headroom, because suburban cannot hold
three production-sized database nodes. Phase 3 scales the testbed engine
memory flags down proportionally and records the prod values separately.

Production (vault), pending operator approval:

| Guest | Cores | Memory | Disk |
|---|---|---|---|
| tack-data1/2/3 | 4 | 16384 MB | 60 GB |
| tack-app2 | 4 | 8192 MB | 40 GB |

Disk drops from the proposed 360 GB to 220 GB, leaving 170 GB free on
local-lvm. Memory needs two operator decisions before phase 3 starts services
on these guests:

1. Reclaim the debianct sandbox guest (VMID 100, 8 cores, 16384 MB,
   unmanaged), which the design pre-approves subject to confirmation.
2. Shrink guest 117 from 24576 MB to app-guest size as each data service
   migrates off it.

With both, the end state is about 79 GB committed against 94 GB physical.
Without both, phase 3 would start database nodes into an overcommitted
hypervisor. Phase 2 itself is safe either way: the new guests carry no
compose stack.
