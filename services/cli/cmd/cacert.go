package cmd

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var caCertCmd = &cobra.Command{
	Use:   "ca-cert",
	Short: "Extract local cluster CA certificate for TLS trust",
	Long: `Extracts the Telara local CA certificate from the cluster.
Save the output to a file and pass it via --ca-cert or TELARA_CA_CERT_PATH.

Example:
  telara ca-cert > ~/telara-ca.crt
  export TELARA_CA_CERT_PATH=~/telara-ca.crt
  telara login --token tlrc_...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := exec.Command(
			"kubectl", "get", "secret", "telara-local-ca-secret",
			"-n", "telara-middleware",
			"-o", "jsonpath={.data.tls\\.crt}",
		).Output()
		if err != nil {
			return fmt.Errorf("failed to get CA cert: %w\nEnsure kubectl is configured and the local cluster is running", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		if err != nil {
			return fmt.Errorf("failed to decode CA cert: %w", err)
		}
		fmt.Print(string(decoded))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(caCertCmd)
}
