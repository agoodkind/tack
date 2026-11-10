# YugabyteDB networking contract

The default Docker network must remain IPv6-only. Its service names resolve only to
IPv6 addresses. Deployment must not enable IPv4 or dual-stack networking to bypass an IPv6
failure.

The base service must use the stable Docker DNS name `yugabyte` for its database
identity, advertised address, and listen address. Deployment must not derive that identity
with `getent` or substitute a container's global IPv6 address. YugabyteDB stores
its advertised identity in its catalog; a replacement container can receive a
different address.

Deployment overrides must preserve stable node identities. A node must listen
on the identity it advertises, and that name must resolve inside its container
to an address it can bind. An image update must preserve the tserver flagfile
argument and the persistent base directory.

Each image change requires a refreshed patched `yugabyted` overlay. Deployment must pass
the checks in [Update the YugabyteDB image](../../runbooks/deployment/update-yugabyte-image.md)
on QA before production.
