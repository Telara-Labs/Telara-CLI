package auth

import (
	"fmt"
	"strings"
)

const TokenPrefix = "tlrc_"

// ValidateTokenFormat checks that the provided token has the expected prefix
// and minimum length required for a valid Telara CLI token.
func ValidateTokenFormat(token string) error {
	if !strings.HasPrefix(token, TokenPrefix) {
		return fmt.Errorf("invalid token format: must start with %q", TokenPrefix)
	}
	if len(token) < 20 {
		return fmt.Errorf("invalid token: too short")
	}
	return nil
}
