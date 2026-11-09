# Configure the PostgreSQL provider for YugabyteDB

Configure the patched PostgreSQL provider before managing roles on YugabyteDB
2024.2. The provider cannot parse YugabyteDB's engine version string and fails
during capability detection on its first resource operation. The compatible
`expected_version = "11.2.0"` setting below skips detection. The provider still
detects capabilities when `expected_version` equals the default `9.0.0`, even
when the configuration sets that value explicitly.

1. Set `expected_version` to the compatible PostgreSQL version and select external
   role locking:

   ```hcl
   provider "postgresql" {
     expected_version = "11.2.0"
     role_lock_mode   = "external"
     # Supply host, port, database, username, and password.
   }
   ```

2. Configure provider execution with parallelism 1. Serialize every provider
   instance that writes the roles, and acquire real host and backend locks before
   those writes.
3. Omit `roles` from each `postgresql_role` resource and manage membership grants
   through `postgresql_grant_role`. Check the
   [role locking contract](../reference/postgresql-provider-locking.md) for
   resource restrictions and failure recovery behavior.
4. Before the first rename after migration to external mode, refresh state if it
   still lacks `role_oid` and a successful read or update has not recorded one.
   Confirm that state records the OID before applying the rename.
