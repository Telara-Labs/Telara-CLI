package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
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
	// Skipped says why no MCP file was changed on purpose, e.g. the tenant
	// acts only on managed devices and this one has no managed policy.
	Skipped string `json:"skipped,omitempty"`
	// CredentialStrips names the OAuth clients and token fingerprints whose
	// credentials this device takes out of its MCP files (never a value).
	CredentialStrips []string `json:"credential_strips"`

	// strips is the parsed list, for the credential bindings the report
	// carries. Nil when the list was not fetched or did not parse.
	strips []mcpquarantine.CredentialStrip
}

// enforceMCPBlocks takes servers the tenant blocked out of this user's MCP
// client files and puts back any whose block was lifted. Best effort, like
// skill enforcement: the gateway refuses a blocked server on every call, and
// this makes the device agree by the next scan.
func enforceMCPBlocks(endpoint, token string, asJSON bool) *mcpEnforcementReport {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := &mcpEnforcementReport{Blocked: []string{}, Actions: []mcpquarantine.Action{}, Errors: []string{}, CredentialStrips: []string{}}
	warn := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		rep.Complete = false
		rep.Errors = append(rep.Errors, msg)
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", msg)
	}
	client := api.NewClient(endpoint, token)
	bl, err := client.MCPServerBlocklist(ctx)
	value, managedOnly := bl.Value, bl.ManagedOnly
	if err != nil {
		warn("could not fetch blocked MCP servers (%v); no MCP configuration was changed this run", err)
		return rep
	}
	blocked, err := mcpquarantine.ParseBlocklist(value)
	if err != nil {
		warn("%v; no MCP configuration was changed this run", err)
		return rep
	}
	strips, stripErr := mcpquarantine.ParseCredentialStrips(bl.CredentialStrips)
	rep.Checked, rep.Complete, rep.Blocked = true, true, mcpquarantine.Names(blocked)
	if stripErr == nil {
		rep.strips = strips
	}
	if managedOnly && !discovery.LoadManagedMCPPolicy().Present() {
		// The gateway still refuses a blocked server on every governed call.
		rep.Skipped = "your organization acts only on managed devices, and this device has no managed policy"
		if !asJSON {
			fmt.Fprintf(os.Stderr, "MCP client files left unchanged: %s\n", rep.Skipped)
		}
		return rep
	}
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
	escrowMCPEntries(ctx, client, home, blocked, bl.Escrow, rep, asJSON, warn)
	// After the MCP sync, so an entry reinstated this run is stripped this run.
	if stripErr != nil {
		warn("%v; no credential was taken out or put back this run", stripErr)
	} else {
		enforceCredentialStrips(home, strips, rep, asJSON, warn)
	}
	return rep
}

// enforceCredentialStrips takes the credentials of OAuth clients the tenant took
// offline out of this user's MCP files, and puts back those whose takedown was
// lifted (TENG-3921). The token is never revoked at the provider and never sent
// to Telara: it is kept in an owner-only record on this device.
func enforceCredentialStrips(home string, strips []mcpquarantine.CredentialStrip, rep *mcpEnforcementReport, asJSON bool, warn func(string, ...interface{})) {
	for _, s := range strips {
		rep.CredentialStrips = append(rep.CredentialStrips, s.String())
	}
	actions, err := mcpquarantine.SyncCredentials(home, strips, time.Now())
	for _, a := range actions {
		rep.Actions = append(rep.Actions, a)
		if asJSON {
			continue
		}
		switch a.Kind {
		case "credential_quarantined":
			fmt.Fprintf(os.Stderr, "Took a credential out of %s, server %q, %s (taken offline by your organization); kept at %s\n", a.Path, a.Server, a.Note, a.Record)
		case "credential_restored":
			fmt.Fprintf(os.Stderr, "Put a credential back in %s, server %q, %s (takedown lifted)\n", a.Path, a.Server, a.Note)
		case "credential_conflict":
			fmt.Fprintf(os.Stderr, "A credential was not put back in %s, server %q: %s\n", a.Path, a.Server, a.Note)
		case "credential_unhandled":
			fmt.Fprintf(os.Stderr, "A credential in %s, server %q, was not taken out: %s\n", a.Path, a.Server, a.Note)
		}
	}
	if err != nil {
		warn("%v", err)
	}
}

// escrowMCPEntries keeps a credential-free copy of each entry this run took
// offline (when the tenant's device escrow is on), closes the copy of each
// entry put back from the local quarantine, and puts back from escrow an
// unblocked entry whose local record is gone (TENG-3921). An entry restored from
// escrow has no credentials: new ones must be issued.
func escrowMCPEntries(ctx context.Context, client *api.Client, home string, blocked map[string]bool, escrowOn bool, rep *mcpEnforcementReport, asJSON bool, warn func(string, ...interface{})) {
	device := installationKey()
	if escrowOn {
		for _, a := range rep.Actions {
			if a.Kind != "quarantined" || a.Record == "" {
				continue
			}
			rec, err := mcpquarantine.ReadRecord(a.Record)
			if err != nil {
				warn("escrow of %s skipped: %v", a.Server, err)
				continue
			}
			entry, keys, err := mcpquarantine.StripEntry(rec.Entry)
			if err != nil {
				warn("escrow of %s skipped: %v", a.Server, err)
				continue
			}
			if err := client.PutEscrow(ctx, api.EscrowEntry{Installation: device, Server: rec.Server, EntryKey: rec.Key, ClientID: rec.ClientID,
				ItemsKey: rec.ItemsKey, EntryJSON: string(entry), StrippedKeys: keys}); err != nil {
				warn("escrow of %s failed (the local copy is kept): %v", a.Server, err)
			}
		}
	}
	open, err := client.ListEscrow(ctx, device)
	if err != nil {
		warn("could not read this device's escrow: %v", err)
		return
	}
	reinstated := map[string]bool{}
	for _, a := range rep.Actions {
		if a.Kind == "reinstated" {
			reinstated[a.ClientID+"|"+a.Server] = true
		}
	}
	for _, e := range open {
		if blocked[e.Server] {
			continue
		}
		if reinstated[e.ClientID+"|"+e.Server] {
			if err := client.MarkEscrowRestored(ctx, e.EscrowID); err != nil {
				warn("could not close the escrow copy of %s: %v", e.Server, err)
			}
			continue
		}
		if mcpquarantine.HasRecord(home, e.ClientID, e.EntryKey) {
			continue // the local quarantine puts it back
		}
		act, err := mcpquarantine.RestoreFromEscrow(home, e.ClientID, e.ItemsKey, e.EntryKey, json.RawMessage(e.EntryJSON))
		act.Server = e.Server
		if err != nil {
			warn("could not restore %s from escrow: %v", e.Server, err)
			continue
		}
		if act.Kind == "reinstated_from_escrow" {
			act.Note = "credentials were not kept; enter new ones for: " + strings.Join(e.StrippedKeys, ", ")
			if err := client.MarkEscrowRestored(ctx, e.EscrowID); err != nil {
				warn("restored %s but could not close its escrow copy: %v", e.Server, err)
			}
			if !asJSON {
				fmt.Fprintf(os.Stderr, "Put MCP server %q back in %s from escrow; %s\n", e.Server, act.Path, act.Note)
			}
		}
		rep.Actions = append(rep.Actions, act)
	}
}
