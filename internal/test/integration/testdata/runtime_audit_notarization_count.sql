SELECT count(*)
  FROM audit.notarizations
 WHERE signing_host = $1
