//go:build !linux && !android && !darwin

package chargectl

// Start and Stop are no-ops on platforms with no charge-control backend
// (Windows/FreeBSD). macOS has its own ctrl_darwin.go.
func Start()   {}
func Stop()    {}
func Release() {}
