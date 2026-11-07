# Use the patched PostgreSQL provider with YugabyteDB

This module builds the cyrilgdn PostgreSQL provider from a pinned release with a patch that keeps `postgresql_role` state accurate when an update fails. Configure the provider before managing roles on YugabyteDB 2024.2.

Run the provider with parallelism 1, serialize all provider instances, and hold real host and backend locks when using external mode.

## Configure the provider

The provider reads the engine version string to detect capabilities. It cannot parse the YugabyteDB string, and the first resource operation fails during detection. Set the PostgreSQL version that YugabyteDB 2024.2 is compatible with, which makes the provider skip detection:

```hcl
provider "postgresql" {
  expected_version = "11.2.0"
  role_lock_mode   = "external"
  # host, port, database, username, and password as usual
}
```

## Set `role_lock_mode`

The `role_lock_mode` provider setting accepts `advisory` (the default) and `external`. Any other value fails validation.

In `advisory` mode, `postgresql_role` update and delete take PostgreSQL advisory locks, and create takes none. YugabyteDB does not accept those locks. A role update on YugabyteDB fails and leaves the prior password version in state. `TestYugabyteAdvisoryLockFailure` covers this behavior.

In `external` mode, `postgresql_role` create, update, and delete take no database lock. The provider serializes those writes inside its own process. Before adopting this mode, run the provider with parallelism 1, serialize all provider instances, and hold real host and backend locks.

External mode changes these behaviors:

| Behavior | Detail |
|---|---|
| Memberships | The resource reads memberships but does not reconcile them or issue membership grants or revocations. Deletion runs ownership reassignment with the administrator permissions and fails if those permissions are insufficient. A configured `roles` attribute fails with an error, and an omitted `roles` attribute leaves existing memberships in place. Use `postgresql_grant_role` for grants. |
| Identity | The `role_oid` attribute records the database OID of the role on create, import, and read. The provider finds the role by that OID after a rename. |
| Replaced role | When the recorded OID no longer exists and another role has the managed name, the provider fails with a role identity conflict and does not adopt the other role. |
| State without an OID | The provider locates the role by name when state lacks an OID. Before the first rename, refresh state if it still lacks an OID and a successful read or update has not recorded one since migration to external mode. |
| Write-only password recovery | The provider records the role name when it applies a write-only password. If state lacks that record and the supplied password is nonempty, the next apply reapplies the password and records the name. The provider treats an empty password as absent. After an interrupted rename, the next apply restores that password without changing its version. |
| Failed update | The update keeps the prior password version in state. YugabyteDB DDL that finished before the failure stays in effect. The next refresh follows the OID to the actual role name. |

## Know the remaining advisory locks

External mode applies only to `postgresql_role`. The `postgresql_database`, `postgresql_default_privileges`, and `postgresql_grant` resources still take advisory locks on the role or database, and they fail on YugabyteDB.
