package audit

// needsDurableAdmission identifies auth.token_used because the audit consumer
// uses it to update the API token's last use time.
func needsDurableAdmission(verb Verb) bool { return verb == VerbAuthTokenUsed }
