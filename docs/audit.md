# Audit policy

Tack audits actions requested through its public interfaces. This includes
requests from people, agents, and API tokens.

## Audited actions

Audit these classes of requested action:

- Access to user data, including reading, listing, searching, and exporting.
- Changes to user data, relationships, accounts, credentials, and permissions.
- Administrative actions invoked through public APIs or operator commands.

Record the caller, requested action, target, and time of the attempt.
An attempt record does not claim that the action succeeded. Audit records
do not require success or failure outcomes.

## Data changes

For changes made by an audited action, preserve the affected records and
their actual old and new values, including removals. Commit change evidence
with the database change.

When a job performs work for a requested action, retain the original caller
and operation. Associate resulting data changes with that operation.

## Internal work

Internal work does not create additional compliance actions. Exclude
autonomous background jobs, internal service calls, indexing, retries,
polling, health checks, and protocol discovery. This exclusion includes
internal reads of user data.

## Recording requirements

Save the attempt before executing the requested action. Refuse execution
when Tack cannot save the required attempt record.

Initial installation may defer recording while it creates the required
audit storage. Operator identity remains required. Ordinary runtime
authentication or storage failures do not qualify for this exception.

Exclude credentials and secret values from audit records. Preserve
existing protection and redaction requirements for personal data.

Preserve historical records and their verification rules. Do not rewrite
earlier records to match this policy or reconstruct missing historical
values.
