# Find current sources

Read code and configuration for current keys, schemas, environment variables, services, and hosts.

1. Use the owning source when checking current values.

   Source references:

   | Subject | Required source |
   | --- | --- |
   | FoundationDB key families | [Key definitions](../../internal/adapters/foundationdb/keys.go) |
   | Authentication and audit SQL schema | [Migrations](../../migrations/) |
   | Server environment variables | [Configuration](../../internal/config/config.go) |
   | Container services and mounts | [Compose stack](../../docker-compose.yml) |

2. Run `./server ops help` and read [ops](../../internal/ops/) for implementation details.
3. Read the Configs [OpenTofu roots and modules](https://github.com/agoodkind/configs/tree/main/opentofu) for guest and network declarations.
4. Query TACK issues through the MCP tools.
5. Read the session memory handoff. Verify mutable claims against current sources.
