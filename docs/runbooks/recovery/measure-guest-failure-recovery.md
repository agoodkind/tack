# Measure guest-failure recovery

Use the [acceptance criteria](../../reference/recovery/2026-08-08-backup-rearch-acceptance.md)
for the required result of each measurement. Run deliberate guest failures on
QA. Store observations outside the guest under test.

1. Record cluster status, full replication, a successful public product write,
   and the current restorable point. Preserve the acknowledged value for
   readback after the failure.
2. Poll external health once per second for two minutes while stopping one data
   guest. Exercise public product reads and writes throughout that interval.
   Record the failure measurements against their comparison points.

   Failure measurements:

   | Subject | Measurement | Comparison |
   | --- | --- | --- |
   | External health | Measure the longest interval with non-200 responses. | Require at most ten seconds. |
   | Acknowledged pre-failure write | Read back the write after the failure. | Compare with the acknowledged value from step 1. |
   | Ledger election | Record the election time. | Apply the acceptance criteria. |

3. Observe the restorable point during the failure and follow backup status for
   fifteen minutes. Require continued advancement, rather than a single fresh
   sample after recovery.
4. Restore the guest and record the time until every cluster reports full
   replication. Repeat for each data guest. Measure each affected database and
   broker; a ledger-only test cannot establish product-store availability.
5. Record the measurements for the affected broker and app services.

   Service measurements:

   | Failure | Measurement | Comparison |
   | --- | --- | --- |
   | Broker failure | Count produced events and their permanent ledger rows. | Compare both counts. |
   | App failure | Observe proxy selection and external responses. | Apply the acceptance criteria. |

6. Retain the before-and-after measurements with the corresponding guest and
   engine identities.
