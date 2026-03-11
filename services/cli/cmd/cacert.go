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
	Long: `Extracts the Telera local CA certificate from the cluster.
Save the output to a file and pass it via --ca-cert or TELERA_CA_CERT_PATH.

Example:
  telara ca-cert > ~/telera-ca.crt
  export TELERA_CA_CERT_PATH=~/telera-ca.crt
  telara login --token tlrc_...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := exec.Command(
			"kubectl", "get", "secret", "telera-local-ca-secret",
			"-n", "telera-middleware",
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
