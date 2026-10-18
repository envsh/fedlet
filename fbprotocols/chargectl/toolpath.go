package chargectl

import (
	"os"
	"os/exec"
	"path/filepath"
)

// toolSearchDirs are extra directories probed for the external macOS tools in
// addition to $PATH: a root/daemon launch often has a minimal PATH, so a tool
// installed via Homebrew/MacPorts would otherwise be missed.
var toolSearchDirs = []string{
	"/usr/local/bin",
	"/opt/homebrew/bin",
	"/opt/local/bin",
	"/usr/local/sbin",
	"/usr/bin",
}

// findTool returns the path of an executable named name, trying $PATH first and
// then toolSearchDirs. It returns "" when nothing executable is found.
func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range toolSearchDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
