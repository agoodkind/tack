# YugabyteDB networking contract

Keep the default Docker network IPv6-only. Its service names resolve only to
IPv6 addresses. Do not enable IPv4 or dual-stack networking to bypass an IPv6
failure.

Use the stable Docker DNS name `yugabyte` for the base service's database
identity, advertised address, and listen address. Do not derive its identity
with `getent` or substitute a container's global IPv6 address. YugabyteDB stores
its advertised identity in its catalog; a replacement container can receive a
different address.

Deployment overrides must preserve stable node identities. A node must listen
on the identity it advertises, and that name must resolve inside its container
to an address it can bind. An image update must preserve the tserver flagfile
argument and the persistent base directory.

Refresh the patched `yugabyted` overlay after every image change and validate the
result on QA before production. Follow [Update the YugabyteDB image](../../runbooks/deployment/update-yugabyte-image.md)
for the required checks.
