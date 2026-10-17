//go:build !linux && !android

package chargectl

// Start and Stop are no-ops on platforms without the sysfs power-supply tree.
// The package still compiles so fedbridge/chargectl.go needs no build tag.
func Start() {}
func Stop()  {}
