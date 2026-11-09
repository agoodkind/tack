# How node references stay unique

Every node has a canonical UUID. New nodes use UUIDv7, which sorts by its millisecond timestamp. Human-readable references such as `FAN-13` address nodes. Renaming a reference does not change a node's UUID or the UUIDs that its relationships, comments, activity, workflow properties and ledger rows use.

`TypeKey` identifies a node type even when its display names change. The type's features and property definitions determine which operations and values it supports. Its singular and plural slugs supply tool and display names. In the seed, the `slug` property addresses a workspace and `identifier` addresses a project. Other types can declare a different property for direct addressing. A scope-local default stores a string such as `Todo`. At creation, the service matches the declared property against children of the selected scope and stores the matching UUID. An ambiguous default must fail. A scope-local default cannot select a target by a generated sequence reference such as `FAN-13`.

## Templates and counters

A `NodeType` declares an ordered list of reference templates. The contract requires a unique name for each template in the list. For example, a type can have both a public reference and a unique external tracker identifier. Storage keys and conflict errors include the template name. Each template renders its parts in order. The reference contract requires exactly one primary template for the human-readable reference when the list is nonempty.
The `IsPrimary` field documents a limit of one, but each template stores its own flag. The FoundationDB setter does not reject multiple primary flags. `PrimaryReferenceTemplate` returns the first marked template, or nil if none is marked.
The reference contract permits a type without a template. Such a type must not produce a uniqueness claim or a rendered reference. The engine must not invent an implicit template or part. Seed data can differ between deployments.

The template can render a composite identifier that a uniqueness flag on a single property cannot express. It specifies the value to print, how to parse it, the value to claim for uniqueness and the value to look up.
`Generated` selects the property to fill during creation. An empty `Generated` does not select a property for template allocation.
A template that fails to render a required part must report the problem instead of issuing a partial reference.

The template uses each part's value to render its reference.

Reference parts:

| Part | Value |
| --- | --- |
| `scope_ref` | The design requires the rendered reference of the nearest ancestor that declares the requested feature. For example, a project with a direct identifier can supply `FAN`. |
| `property` | The declared property supplies a nonempty string such as `In Progress` or a number such as `13`. Rendering preserves the JSON digits of a number. |
| `node_type` | The node's stable `TypeKey` distinguishes types that use the same number. |
| `literal` | The configured text supplies a prefix or separator. |

The service starts at the selected scope and follows `parent_id`. It uses the first ancestor that declares the requested feature and supplies a nonempty `direct_property` address. It can skip a nearer ancestor with a composite reference and use a farther ancestor's direct address. An invalid direct-address value fails decoding. Rendering fails if the walk does not supply the required feature's address.

The contract permits scope chains of arbitrary depth. The current service returns an error if the chain continues after 32 nodes.

For the `scoped_sequence` strategy, parsing must recover the scope and generated property from the same parts. With a hyphen separator, `FAN-QA-13` yields scope `FAN-QA` and value `13`. If the template also includes the type, `FAN-epic-1` yields scope `FAN`, type `epic` and value `1`. A missing separator or required segment fails parsing.

For a `scoped_property` such as `PROJECT::In Progress`, the example template declares `scope_ref(is_scope)`, `literal("::")` and `property(name)` in that order. A direct property needs only its declared value, such as `property(identifier)`.

In the `uuid_only` example, the resolver accepts a UUID and the strategy does not render a human-readable reference. The type does not declare any reference templates, so creation does not claim an address.

A template with zero parts renders the empty string. A create that passes its other checks supplies that string as a claim under the template name in its organization. The transaction writes the claim when it is available and returns `ErrConflict` if a different node owns it.

The generator omits the property it fills and renders the remaining parts to obtain the counter key. The allocator selects the counter by organization and rendered counter key. Types using the seeded template in the same organization share a counter when their `scope_ref` parts render the same text. A `node_type` part distinguishes both the counter and the printed value. For example, with `OPS` as the scope, a hyphen separator and `number` as the generated property, the counter key is `OPS-`. A value of 4 renders as `OPS-4`. Each allocation increments the selected counter atomically. When a deployment adopts a different counter key for existing nodes, the repair must initialize it to at least the highest value already present before issuing new numbers. A new counter begins at zero. A repeat initialization must never lower its value.

The seed design requires a shared primary template for issues, epics, cycles and modules. Its parts select the `is_scope` feature, the literal `-` and the generated property `sequence`. It omits `node_type`. Issue and epic creates in one project take successive numbers.
The current seed installs this template only on types with `has_sequence_id`. Issues and epics declare that feature. Cycles and modules use the `scoped_sequence` strategy but omit the feature and the template.
Separators, scope features, property names and type keys are deployment data.

Each time `SeedOrg` runs, it writes the full built-in type definitions again. Reseeding restores an edited or removed template on a built-in type to its seed declaration. This loop does not rewrite organization-defined types. An organization can keep a custom template across reseeds on its own type.

The current MCP renderer chooses a format from the reference strategy without consulting the template list. Its strategy formatter returns an empty string for `uuid_only`. When that formatter returns an empty string, the outer display function tries the `identifier` and `slug` properties, then the node name. These display values do not create reference claims.

For a scoped sequence, the strategy formatter formats the configured property with a hyphen even if the type lacks a template. Without a scope identifier it can print the uppercased type key. Without a positive sequence it tries a scoped name.

When a create supplies a scope and the primary template is missing or its `Generated` field is empty, the service checks `has_sequence_id`. Only types with that feature receive a legacy `sequence` from the organization, scope and type counter.

The legacy numbering and display paths do not implement arbitrary template rendering.

## Claims and lookups

FoundationDB packs the organization, template name and complete rendered string into the forward `node_reference` key. Its value identifies the owning node. The reverse `node_reference_owned` key selects that node and template and stores the rendered string. It lets deletion and updates find all prior claims by UUID without reconstructing their templates. The tuple contains one string for the entire rendered value.

```
(node_reference,       orgID, templateName, encodedKey) -> nodeID
(node_reference_owned, orgID, nodeID, templateName)     -> encodedKey
```

A create reads the forward claim in the mutation transaction. A different owner produces `ErrConflict`. The same owner may assert its claim again. An update replacing its claims clears previous forward and reverse entries before writing new ones in the same transaction. An explicit empty replacement releases the claims. An omitted replacement leaves existing claims alone. A deletion releases the node's claims. A malformed reverse entry must fail the transaction, since clearing it could strand a forward claim.

The service renders claims from the type's metadata. `NodeService` uses `node.Mutator.ApplyMutation` for a user-facing create or update. The transaction reads the current type and property definitions, derives the indexed properties and accepts the service's rendered claims. It commits the primary record and its properties, resolution record, view, indexes, claims, relationship changes and required audit evidence together. When the store enables search work, the transaction also schedules the required indexing. A move into a scope where another node owns the new reference must fail without changing either node.

The design requires a human-readable reference to resolve with one point read, a lookup of one exact storage key. For sequence references, it rules out scanning the property index, walking each candidate's parent chain and trying every sequence-bearing type in each workspace.

A typed numeric lookup uses only the requested type. The untyped resolver tries the types that declare `has_sequence_id`. For each workspace in the caller's member organizations, the resolver checks the eligible primary templates until it finds a claim. Each call to `NodeStore.LookupReference` reads one forward key for the organization, template name and input. This lookup does not scan the property index or walk a candidate's ancestors. Resolving one input can require several key reads.

A missing claim returns `uuid.Nil` and a nil error. The resolver can try the remaining candidates. A hit records the owning organization in the audit context and returns the node ID from the claim. The requested type selects the template, but a typed numeric hit does not reread the owner to verify its type. A storage error stops the typed numeric lookup. If every lookup misses, the typed resolver returns not found.

Workspace enumeration skips an organization if listing its workspaces fails. A not-found result therefore cannot establish that the resolver checked every member organization.

The untyped resolver preserves invalid-input errors and translates other sequence lookup errors, including storage errors, into not found.

For a `scoped_sequence` input without `::`, the numeric parser requires a nonempty scope and a positive integer. With a primary template, it reads the `is_scope` part and the property selected by `Reference.Property`, which defaults to `sequence` when empty. If none of the eligible types has a primary template, it can parse the legacy form at the last hyphen. Successful parsing alone cannot resolve a number without a matching claim.

For a typed `scoped_sequence` input containing `::`, the resolver instead looks up a scoped name. It requires text on both sides of the first `::`, resolves the scope, and matches the local name on a node of the requested type. This lookup does not require a sequence number or a forward claim.

A `direct_property` lookup uses the type's declared property. A `scoped_property` lookup matches its declared property within the selected scope. A single match identifies the node. An absent match returns not found. Multiple matches must return an ambiguity error with the candidate UUIDs in sorted order.

A typed UUID lookup checks the node's actual type and requires membership in its organization before returning the ID.

List and search output must display human-readable cross-references and omit raw UUIDs for referenced nodes, including parents and workflow states. A detailed node response displays the node's own canonical UUID in its `Raw id` field.
