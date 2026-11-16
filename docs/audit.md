# Audit policy

Tack audits actions requested through its public interfaces. This includes
requests from people, agents, and API tokens.

## Audited actions

Tack audits these classes of requested action:

- Access to user data, including reading, listing, searching, and exporting.
- Changes to user data, relationships, accounts, credentials, and permissions.
- Administrative actions invoked through public APIs or operator commands.

Each attempt record identifies the caller, requested action, target, and time
of the attempt. An attempt record does not claim that the action succeeded.
Audit records do not require success or failure outcomes.

## Data changes

For each audited action that changes data, Tack preserves the affected
records and their actual old and new values, including removals. Tack commits
the change evidence with the database change.

When a job performs work for a requested action, Tack retains the original
caller and operation. Tack associates the resulting data changes with that
operation.

## Internal work

Tack does not audit internal work that runs or maintains its services.
Examples include:

- Autonomous background jobs.
- Calls between internal services.
- Search indexing.
- Retries and polling.
- Health checks and protocol discovery.

This exclusion also applies when internal work reads user data.

## Recording requirements

Tack saves the attempt before executing the requested action. It refuses
execution when it cannot save the required attempt record.

Initial installation may defer recording while it creates the required
audit storage. Operator identity remains required. Ordinary runtime
authentication or storage failures do not qualify for this exception.

Audit records do not include credentials or secret values. Tack protects
and redacts personal data according to the existing requirements.

Tack preserves historical records and their verification rules. It does not
rewrite earlier records to match this policy or reconstruct missing
historical values.
