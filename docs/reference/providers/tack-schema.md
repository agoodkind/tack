# tack_schema resource reference

The Tack provider currently registers `tack_schema` for the YugabyteDB migration
version. It uses the pveguest transport to run `migrate --output json` in the
guest's `tack-ops` Compose service. Status reads omit `--execute` and use `--pull never`.
The resource does not provision the guest or the complete stack.

## Resource identity and state

The resource identifies an installation as `<node>/<vmid>:<install_dir>`.
Import records `node`, `vmid`, and `install_dir`; refresh reads the live migration
version. The provider reports unreadable status as an error and preserves state.
If the Goose migration table does not exist, the provider removes the resource
from state. Delete only removes the resource from state and does not revert migrations.

## Migration planning and apply

- Planning rejects a target below the applied version.
- Planning rejects a target above the image's highest available migration.
- A target at or below version 19 requires an applied version of at least 19.
- Planning marks `pending` as unknown until apply when the target differs from the applied version.
- Planning lists the required migrations in a warning when the target differs from the applied version.
- Apply rejects a changed image commit.
- Apply runs `migrate --execute --to` with `--expect-from`.
- Apply verifies that the final version matches the target.
- Apply verifies pending migrations through the target.
- A nonzero `--expect-from` requires the matching starting version.
- An `--expect-from` value of zero does not require a matching starting version.

## Audit identity

- The provider requires an operator email.
- The provider derives the operator's version 5 UUID from the trimmed, lowercased email.
- Configs uses the same namespace and lowercased email in its [release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf).
- An agent identity requires both `service` and `session`.
- A person identity uses `name`.
- Every Tack command receives the corresponding audit identity flags.
