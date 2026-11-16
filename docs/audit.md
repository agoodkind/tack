# Audit policy

Tack records actions that people or agents request through its APIs and operator
commands, including requests authenticated with API tokens. Audit records
identify who requested an action and preserve evidence of any resulting data
changes.

## Audited actions

Tack audits these requested actions:

- Reading, listing, searching, or exporting user data.
- Changing user data, relationships, accounts, credentials, or permissions.
- Performing administrative work through public APIs or operator commands.

## Attempt records

Before executing an audited action, Tack saves an attempt record with:

- The caller who requested the action.
- The requested action.
- The target data or resource.
- The time of the attempt.

Tack refuses execution if it cannot save this record. An attempt record does
not establish that the action succeeded, and audit records do not require
success or failure outcomes.

## Data changes

Change evidence includes the affected records and their actual old and new
values, including removals. Tack saves a database change and its evidence
together or saves neither.

Jobs that perform a requested action associate resulting data changes with
the original caller and action.

An example request changes the title of issue 123:

| Evidence | Example |
| --- | --- |
| Attempt | Sam requests renaming issue 123 at 10:00. |
| Data change | Tack saves the old title “Draft” and new title “Ready” with the change. |

## Internal work

Tack does not audit internal work that runs or maintains its services.
Examples include:

- Background jobs that Tack starts without a caller's request.
- Calls between internal services.
- Search indexing.
- Internal retries and repeated checks for new work, called polling.
- Service health checks and discovery of available protocol methods or tools.

This exclusion also applies when internal work reads user data.

## Initial installation

Initial installation may defer recording while it creates the required audit
storage. Operator identity remains required during installation. Authentication
or storage failures during ordinary operation do not qualify for this exception.

## Protected data

Audit records do not include credentials or secret values. Existing personal
data protection and redaction requirements continue to apply.

## Historical records

Earlier audit records retain their original verification rules. Tack does not
rewrite these records to match this policy or reconstruct historical values
it never recorded.
