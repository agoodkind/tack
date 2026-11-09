# YugabyteDB port checks on IPv6-only networks

The base YugabyteDB service advertises the Docker DNS name `yugabyte`. Docker
resolves that name to an IPv6 address on the default network.

The upstream launcher represented by the checked-in patch chooses an IPv4
socket when the advertised identity is a hostname rather than an IPv6 literal.
That socket cannot check a hostname that resolves only to IPv6.

The patched `yugabyted` launcher applies the address-family change from
[upstream PR #23158](https://github.com/yugabyte/yugabyte-db/pull/23158).
It asks `getaddrinfo` for either address family with `AF_UNSPEC`, creates a
socket for the returned family, and checks the resolved address. Compose
mounts the launcher read-only over the engine's launcher. The restore drill
also uses the overlay for its temporary engine.

The secret scanners allow the vendored launcher because its upstream text
contains public default constants. The allowlists do not permit real
credentials in the overlay.
