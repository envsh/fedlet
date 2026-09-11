//go:build !(linux && cgo) && !windows && !(darwin && cgo)

package main

// clipOwner is a no-op on platforms without an implementation.
func clipOwner() (int, string, string) {
	return 0, "", ""
}
