package cmd

import (
	"fmt"

	"gitlab.com/teleraai/telara-cli/services/cli/internal/clicontext"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
)

// newContextStore is a convenience wrapper that opens the context store located
// in the platform-specific CLI config directory.
func newContextStore() (*clicontext.Store, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine config directory: %w", err)
	}
	return clicontext.NewStoreAt(dir)
}
