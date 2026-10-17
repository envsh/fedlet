//go:build linux || android

package chargectl

import (
	"errors"

	"golang.org/x/sys/unix"
)

// probeAccess reports whether path is writable. A failure is logged with the
// errno so a locked-down Android sysfs can be diagnosed from the log alone —
// on many devices the node exists and reads fine but SELinux denies writes.
func probeAccess(path string) bool {
	err := unix.Access(path, unix.W_OK)
	if err == nil {
		return true
	}
	logf("skip %s: %s", path, errReason(err))
	return false
}

// errReason turns an errno from Access into a short human reason.
func errReason(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, unix.EACCES):
		return "permission denied (EACCES)"
	case errors.Is(err, unix.EPERM):
		return "operation not permitted (EPERM)"
	case errors.Is(err, unix.EROFS):
		return "read-only filesystem (EROFS)"
	case errors.Is(err, unix.ENOENT):
		return "no such file (ENOENT)"
	case errors.Is(err, unix.ENOTDIR):
		return "not a directory (ENOTDIR)"
	default:
		return err.Error()
	}
}
