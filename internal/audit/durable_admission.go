package audit

// needsDurableAdmission selects auth.token_used for recording before the
// recorder returns. The audit consumer uses it to update the API token's
// last use time.
func needsDurableAdmission(verb Verb) bool { return verb == VerbAuthTokenUsed }
