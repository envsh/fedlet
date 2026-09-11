//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	user32                     = syscall.NewLazyDLL("user32.dll")
	clipboardOwner             = user32.NewProc("GetClipboardOwner")
	openClipboardWindow        = user32.NewProc("GetOpenClipboardWindow")
	threadWindowProc           = kernel32.NewProc("GetWindowThreadProcessId")
	processFromID              = kernel32.NewProc("OpenProcess")
	queryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	closeHandle                = kernel32.NewProc("CloseHandle")
)

const processQueryLimitedInformation = 0x1000

// clipOwner resolves the clipboard owner HWND to a local PID, short process
// name and image path. Hidden message windows resolve to their host process.
func clipOwner() (int, string, string) {
	h, _, _ := clipboardOwner.Call()
	if h == 0 {
		h, _, _ = openClipboardWindow.Call()
	}
	if h == 0 {
		return 0, "", ""
	}
	var pid uint32
	threadWindowProc.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return 0, "", ""
	}
	proc, _, _ := processFromID.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if proc == 0 {
		return 0, "", ""
	}
	defer closeHandle.Call(proc)

	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := queryFullProcessImageNameW.Call(proc, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 || n == 0 {
		return int(pid), "", ""
	}
	p := syscall.UTF16ToString(buf[:n])
	return int(pid), filepath.Base(p), p
}
