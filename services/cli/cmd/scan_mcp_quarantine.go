package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/mcpquarantine"
)

// mcpEnforcementReport is what a scan says about blocked MCP servers
// (TENG-3921). Complete is true only when the blocklist was fetched and every
// change it called for was made.
type mcpEnforcementReport struct {
	Checked  bool                   `json:"checked"`
	Complete bool                   `json:"complete"`
	Blocked  []string               `json:"blocked"`
	Actions  []mcpquarantine.Action `json:"actions"`
	Errors   []string               `json:"errors"`
}

// enforceMCPBlocks takes servers the tenant blocked out of this user's MCP
// client files and puts back any whose block was lifted. Best effort, like
// skill enforcement: the gateway refuses a blocked server on every call, and
// this makes the device agree by the next scan.
func enforceMCPBlocks(endpoint, token string, asJSON bool) *mcpEnforcementReport {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := &mcpEnforcementReport{Blocked: []string{}, Actions: []mcpquarantine.Action{}, Errors: []string{}}
	warn := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		rep.Complete = false
		rep.Errors = append(rep.Errors, msg)
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", msg)
	}
	value, _, err := api.NewClient(endpoint, token).MCPServerBlocklist(ctx)
	if err != nil {
		warn("could not fetch blocked MCP servers (%v); no MCP configuration was changed this run", err)
		return rep
	}
	blocked, err := mcpquarantine.ParseBlocklist(value)
	if err != nil {
		warn("%v; no MCP configuration was changed this run", err)
		return rep
	}
	rep.Checked, rep.Complete, rep.Blocked = true, true, mcpquarantine.Names(blocked)
	home, err := os.UserHomeDir()
	if err != nil {
		warn("cannot resolve home directory; no MCP configuration was changed")
		return rep
	}
	actions, err := mcpquarantine.Sync(home, blocked, time.Now())
	for _, a := range actions {
		rep.Actions = append(rep.Actions, a)
		if !asJSON {
			switch a.Kind {
			case "quarantined":
				fmt.Fprintf(os.Stderr, "Took MCP server %q out of %s (blocked by your organization); kept at %s\n", a.Server, a.Path, a.Record)
			case "reinstated":
				fmt.Fprintf(os.Stderr, "Put MCP server %q back in %s (block lifted)\n", a.Server, a.Path)
			case "conflict":
				fmt.Fprintf(os.Stderr, "MCP server %q was not put back in %s: %s\n", a.Server, a.Path, a.Note)
			}
		}
	}
	if err != nil {
		warn("%v", err)
	}
	return rep
}
