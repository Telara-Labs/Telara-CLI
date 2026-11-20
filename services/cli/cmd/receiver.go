package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/dpop"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/receiver"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/schedule"
)

// telara receiver (TENG-3921, s4-01): the laptop remediation receiver. See
// internal/receiver for what it does and does not do.

var (
	receiverNoService bool
	receiverMaxActs   int
)

func receiverKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".telara", "receiver", "keys", "receiver-dpop-key.pem"), nil
}

// receiverClient is the person's own login plus this device's key.
func receiverClient(create bool) (*api.Client, error) {
	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return nil, fmt.Errorf("not logged in — run: telara login")
	}
	path, err := receiverKeyPath()
	if err != nil {
		return nil, err
	}
	var key *dpop.Key
	if create {
		// A re-install keeps the same key and registers it again.
		key, err = dpop.LoadOrCreate(path)
	} else {
		key, err = dpop.Load(path)
	}
	if err != nil {
		if create {
			return nil, fmt.Errorf("create the device key: %w", err)
		}
		return nil, fmt.Errorf("no device key — run: telara receiver install (%w)", err)
	}
	client := api.NewClient(prefs.APIURL, token)
	client.Prover = key
	return client, nil
}

var receiverCmd = &cobra.Command{
	Use:   "receiver",
	Short: "Apply MCP server and skill removals on this device as soon as they are made",
	Long: `The receiver keeps an outbound connection to Telara, signed by this device's own
key, and runs telara scan's enforcement as soon as what this device must remove
changes, and at least every five minutes. Without it, removals reach this device
at the next daily scan. It changes nothing telara scan would not.`,
}

var receiverInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Make this device's key, register it with Telara, and start the receiver service",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := receiverClient(true)
		if err != nil {
			return err
		}
		if err := client.RegisterEstateReceiver(context.Background(), installationKey()); err != nil {
			return fmt.Errorf("register the receiver: %w", err)
		}
		fmt.Println("Registered this device's receiver key with Telara.")
		if receiverNoService {
			return nil
		}
		st, err := schedule.InstallReceiver()
		if err != nil {
			return err
		}
		fmt.Printf("Receiver service installed: %s\n", st.Path)
		return nil
	},
}

var receiverRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Hold the receiver's connection in the foreground",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := receiverClient(false)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		r := &receiver.Receiver{
			Control:         client,
			InstallationKey: installationKey(),
			MaxActs:         receiverMaxActs,
			Log:             os.Stderr,
			// The scan itself: enforcement first, then the report, so Telara
			// sees the device as it now is.
			Act: func(ctx context.Context) error {
				scan := exec.CommandContext(ctx, exe, "scan", "--json")
				scan.Stdout, scan.Stderr = os.Stderr, os.Stderr
				return scan.Run()
			},
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = r.Run(ctx)
		if errors.Is(err, receiver.ErrNotBound) {
			// Logged out or re-installed elsewhere: exit cleanly so the
			// service manager does not restart it.
			fmt.Fprintln(os.Stderr, err)
			return nil
		}
		return err
	},
}

var receiverUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the receiver service and this device's key",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := schedule.UninstallReceiver(); err != nil {
			return err
		}
		path, err := receiverKeyPath()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("Receiver removed. Telara no longer accepts this device's old key once you run telara logout or install again.")
		return nil
	},
}

func init() {
	receiverInstallCmd.Flags().BoolVar(&receiverNoService, "no-service", false, "register the key without installing the background service")
	receiverRunCmd.Flags().IntVar(&receiverMaxActs, "max-acts", 0, "stop after this many enforcement runs (0 runs until stopped)")
	_ = receiverRunCmd.Flags().MarkHidden("max-acts")
	receiverCmd.AddCommand(receiverInstallCmd, receiverRunCmd, receiverUninstallCmd)
	rootCmd.AddCommand(receiverCmd)
}
