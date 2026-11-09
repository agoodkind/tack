# PostgreSQL provider role locking

The PostgreSQL overlay builds the cyrilgdn provider from a pinned release and
applies a patch that preserves `postgresql_role` state after a failed update.
The patch supports role management on YugabyteDB 2024.2 with external locking.

## Lock modes

The `role_lock_mode` provider setting accepts `advisory` and `external`. It defaults
to `advisory` and rejects other values.

The modes control locks for `postgresql_role` writes:

| Mode | Create | Update and delete |
| --- | --- | --- |
| `advisory` | The provider does not take a database lock. | The provider takes PostgreSQL advisory locks. |
| `external` | The provider does not take a database lock. | The provider does not take a database lock. |

YugabyteDB does not accept the advisory locks. A role update in advisory mode fails
and preserves the prior password version in state.
[TestYugabyteAdvisoryLockFailure](../../internal/test/pgoverlay/yugabyte_test.go)
requires that result.

External mode serializes role creation, updates, and deletion within one provider
process. Follow
[Configure the PostgreSQL provider](../runbooks/configure-postgresql-provider.md)
before adopting this mode.

## Role state and recovery

External mode changes membership management, role identity, and password recovery.
An OID is the database identifier for a role.

Role behavior:

| Behavior | Detail |
| --- | --- |
| Memberships | The resource reads memberships but does not reconcile them or issue membership grants or revocations. The provider rejects an explicit `roles` attribute. Omitting `roles` preserves existing memberships. |
| Deletion | The resource uses the administrator's permissions for ownership reassignment and fails when those permissions do not suffice. `skip_reassign_owned` omits reassignment and `DROP OWNED`; `skip_drop_role` omits `DROP ROLE`. |
| Identity | The `role_oid` attribute records the database OID on create, import, and read. The provider finds the role by that OID after a rename. |
| Replaced role | If the recorded OID no longer exists and another role has the managed name, the provider reports a role identity conflict and refuses to adopt the replacement. |
| State without an OID | The provider locates the role by name when state lacks an OID. A successful read or update records the OID. A rename requires that recorded identity. |
| Write-only password recovery | The provider records the role name in `password_wo_applied_name` when it applies a write-only password. If state lacks that record and the supplied password is nonempty, the next apply reapplies the password and records the name. The provider treats an empty password as absent. After an interrupted rename, the next apply restores that password without changing its version. |
| Failed update | The update preserves the prior password version in state. YugabyteDB keeps database definition statements that completed before the failure. The next refresh follows the OID to the actual role name. |

## Remaining advisory locks

External mode applies only to `postgresql_role`. The `postgresql_database`,
`postgresql_default_privileges`, and `postgresql_grant` resources still request
advisory locks on the role or database. Operations that request those locks fail
on YugabyteDB.
