package clicontext

import "os"

const envVar = "TELARA_CONTEXT"

// Resolve returns the active context name using this priority:
//  1. flagValue (from --context flag, if non-empty)
//  2. TELARA_CONTEXT environment variable
//  3. prefs.ActiveContext (passed in as prefsActive)
//
// Returns "" if none are set.
func Resolve(flagValue, prefsActive string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return prefsActive
}
