# Phase 2 capacity reading and sizing decision

The 2026-08-08 measurements preceded guest declarations. The measurements used
`free -g`, `pvesm status`, and per-guest `pct config` on both hypervisors.

## Measured capacity

The readings establish capacity before phase 2:

| Hypervisor | Physical RAM | Configured across guests | Datastore free |
|---|---|---|---|
| vault | 94 GB | 58.8 GB (60160 MB across 9 guests) | 390 GB on local-lvm |
| suburban | 31 GB | 13.4 GB (13696 MB across 7 guests) | 1140 GB on local-zfs |

Guest 117 (tack) accounts for 24576 MB of vault's configured guest memory for
all data services. Guest 217 (tack-qa) accounts for 8192 MB for those services. Both shrink to app-guest
size after phases 3 through 5 assign the services to data guests.

## Reject the original sizing

The plan proposed 16384 MB production data guests and 6144 MB testbed data guests.
Neither hypervisor can support that sizing:

- Vault's additions would total 57344 MB, increasing committed memory to 115 GB
  against 94 GB physical. The proposed 360 GB disk allocation would leave
  30 GB free on local-lvm.
- Suburban's additions would total 22528 MB against 17.6 GB of available memory.

Container memory limits set ceilings rather than reservations. Phase 2 does
not start services in the new guests, but phase 3 would start databases on
an overcommitted hypervisor with the original sizing.

## Apply revised testbed sizing

Suburban uses these allocations:

| Guest | Cores | Memory | Disk |
|---|---|---|---|
| tack-data1/2/3 | 2 | 4096 MB | 40 GB |
| tack-app2 | 2 | 2048 MB | 30 GB |

The 14336 MB addition leaves about 3 GB of the 17.6 GB available memory.
The testbed validates topology, replication, failover, and export structure.
It cannot validate production memory capacity because suburban cannot run
three production-sized database nodes. Phase 3 reduces testbed engine memory
flags proportionally and records production values separately.

## Approve production capacity before phase 3

Vault's revised allocations require operator approval:

| Guest | Cores | Memory | Disk |
|---|---|---|---|
| tack-data1/2/3 | 4 | 16384 MB | 60 GB |
| tack-app2 | 4 | 8192 MB | 40 GB |

The revised disk allocation totals 220 GB rather than 360 GB and leaves 170 GB
free on local-lvm. Before phase 3 starts services, require operator decisions to:

1. Reclaim the unmanaged debianct sandbox (VMID 100, 8 cores, 16384 MB).
   The design permits reclamation only after confirmation.
2. Shrink guest 117 from 24576 MB to app-guest size as each data service stops
   running there.

Both changes produce about 79 GB committed against 94 GB physical. Without
both, phase 3 would start databases on an overcommitted hypervisor. Phase 2
does not start compose stacks on the new guests and does not require the changes.
