# Use the patched PostgreSQL provider with YugabyteDB

This module builds the cyrilgdn PostgreSQL provider from a pinned release with a patch that preserves `postgresql_role` state after a failed update. YugabyteDB 2024.2 requires `expected_version`. YugabyteDB rejects the advisory lock that the provider requests during role updates.

## Set `expected_version`

The provider reads the engine version string to detect capabilities. It cannot parse the YugabyteDB string, and the first resource operation fails during detection. Set the PostgreSQL version that YugabyteDB 2024.2 is compatible with, which makes the provider skip detection:

```hcl
provider "postgresql" {
  expected_version = "11.2.0"
  # host, port, database, username, and password as usual
}
```

## Handle role update failures

A `postgresql_role` update on YugabyteDB fails because the provider takes an advisory lock before the update and YugabyteDB does not accept the lock. Role creation works with the `expected_version` setting. The patch does not remove or replace the lock.

A failed update leaves the prior password version in state, and the next plan requests the update again.

`make test-postgresql-overlay` covers this behavior in `TestYugabyteAdvisoryLockFailure`.
