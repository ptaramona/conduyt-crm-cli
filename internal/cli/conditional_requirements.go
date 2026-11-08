package cli

import "fmt"

// PATCH(reprint-637-conditional-requirements): pre-request checks the fresh spec cannot express (it types the
// inputs as optional). Kept in a hand-written file so a reprint only has to restore the one-line calls.

// checkDeleteSessionsTarget: DELETE /auth/sessions revokes one session (--id) or every other session
// (--scope others); exactly one of the two.
func checkDeleteSessionsTarget(idSet bool, id string, scopeSet bool, scope string) error {
	switch {
	case idSet && scopeSet:
		return usageErr(fmt.Errorf("--id and --scope cannot be combined: pass --id <session> or --scope others"))
	case scopeSet:
		if scope != "others" {
			return usageErr(fmt.Errorf("--scope must be \"others\" (got %q)", scope))
		}
	case idSet:
		if id == "" {
			return usageErr(fmt.Errorf("--id must not be empty"))
		}
	default:
		return usageErr(fmt.Errorf("pass --id <session> or --scope others"))
	}
	return nil
}
