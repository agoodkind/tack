# Update required CI checks

Use the checks reported for the pull request's current commit when updating
the main branch ruleset.

1. Wait for the PR workflows to finish successfully.
2. Open the [repository rules](https://github.com/agoodkind/tack/settings/rules)
   and replace obsolete workflow requirements with the required checks from
   the current run.
3. Preserve each check's GitHub App identity and the other branch protections.
4. Confirm that the current commit satisfies the updated ruleset before merging.
