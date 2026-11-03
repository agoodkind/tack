# Use the patched PostgreSQL provider with YugabyteDB

This module builds the cyrilgdn PostgreSQL provider from a pinned release with a patch that keeps `postgresql_role` state accurate when an update fails. YugabyteDB 2024.2 needs one provider setting, and one role operation does not work.

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

A failed update leaves the prior password version in state. The next plan requests the update again, and the update keeps failing until the provider uses a locking method that YugabyteDB supports and that still serializes concurrent updates of the same role.

`make test-postgresql-overlay` covers this behavior in `TestYugabyteAdvisoryLockFailure`.
