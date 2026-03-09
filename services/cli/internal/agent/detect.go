package agent

import (
	"os"
	"os/exec"
)

// binaryInPath returns true if a binary with the given name is found in PATH.
func binaryInPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// dirExists returns true if the given path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
