SELECT pii_ref, payload, redacted
  FROM audit.pii
 WHERE pii_ref = ANY($1::uuid[])
