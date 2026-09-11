//go:build linux && cgo

package main

/*
#cgo LDFLAGS: -lX11 -lXRes
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/extensions/XRes.h>
#include <stdlib.h>

// xres_query_pid returns the server-side PID of the client owning `owner`
// via X-Resource >= 1.2, or 0 when the extension/id is unavailable.
static unsigned long xres_query_pid(Display *dpy, Window owner) {
    XResClientIdSpec spec;
    XResClientIdValue *ids = NULL;
    long num_ids = 0;
    int evbase, errbase;
    int major = 0, minor = 0;

    if (!XResQueryExtension(dpy, &evbase, &errbase)) return 0;
    if (!XResQueryVersion(dpy, &major, &minor)) return 0;
    if (major < 1 || (major == 1 && minor < 2)) return 0; // need client-id (PID)

    spec.client = owner;
    spec.mask = XRES_CLIENT_ID_PID_MASK;
    if (!XResQueryClientIds(dpy, 1, &spec, &num_ids, &ids) || ids == NULL || num_ids < 1) {
        if (ids) XResClientIdsDestroy(num_ids, ids);
        return 0;
    }
    unsigned long pid = (unsigned long)XResGetClientPid(ids);
    XResClientIdsDestroy(num_ids, ids);
    return pid > 1 ? pid : 0;
}
*/
import "C"

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"
)

// clipOwner resolves the CLIPBOARD selection owner window to a local PID,
// short process name and executable path. Best effort: returns zeros/empties
// when attribution is impossible (headless DISPLAY, remote ssh -X clients,
// ownership held by a clipboard manager with no resolvable pid, ...).
func clipOwner() (int, string, string) {
	dpy := C.XOpenDisplay(nil)
	if dpy == nil {
		return 0, "", ""
	}
	defer C.XCloseDisplay(dpy)

	cAtomName := C.CString("CLIPBOARD")
	defer C.free(unsafe.Pointer(cAtomName))
	owner := C.XGetSelectionOwner(dpy, C.XInternAtom(dpy, cAtomName, 0))
	if owner == 0 {
		return 0, "", ""
	}

	pid := x11PidFromPidProperty(dpy, owner)
	if pid == 0 {
		pid = x11PidFromXRes(dpy, owner)
	}
	if pid <= 0 {
		return 0, "", ""
	}
	name, path := procName(pid)
	if name == "" {
		// remote /proc-less pid (e.g. ssh -X) — don't fake a local attribution
		return 0, "", ""
	}
	return pid, name, path
}

// x11PidFromPidProperty walks the owner window and its ancestors looking for
// _NET_WM_PID (set by the app itself, best-effort only).
func x11PidFromPidProperty(dpy *C.Display, owner C.Window) int {
	cPIDName := C.CString("_NET_WM_PID")
	defer C.free(unsafe.Pointer(cPIDName))
	prop := C.XInternAtom(dpy, cPIDName, 1)
	if prop == 0 {
		return 0
	}
	root := C.XDefaultRootWindow(dpy)
	for win := owner; win != 0 && win != root; win = x11Parent(dpy, win) {
		if pid := x11UlongProp(dpy, win, prop, C.XA_CARDINAL); pid > 0 {
			return pid
		}
	}
	return 0
}

func x11Parent(dpy *C.Display, win C.Window) C.Window {
	var root, parent C.Window
	var children *C.Window
	var nchildren C.uint
	ok := C.XQueryTree(dpy, win, &root, &parent, &children, &nchildren)
	if children != nil {
		C.XFree(unsafe.Pointer(children))
	}
	if ok == 0 {
		return 0
	}
	return parent
}

func x11UlongProp(dpy *C.Display, win C.Window, name, reqType C.Atom) int {
	var actualType C.Atom
	var actualFormat C.int
	var nitems, bytesAfter C.ulong
	var data *C.uchar
	ok := C.XGetWindowProperty(dpy, win, name, 0, 1, 0, reqType,
		&actualType, &actualFormat, &nitems, &bytesAfter, &data)
	if ok != 0 || data == nil || actualFormat != 32 || nitems == 0 {
		if data != nil {
			C.XFree(unsafe.Pointer(data))
		}
		return 0
	}
	val := int(*(*C.ulong)(unsafe.Pointer(data)))
	C.XFree(unsafe.Pointer(data))
	return val
}

// x11PidFromXRes uses the X-Resource extension (server-side, trustworthy for
// local clients) as the main attribution path when _NET_WM_PID is absent.
func x11PidFromXRes(dpy *C.Display, owner C.Window) int {
	return int(C.xres_query_pid(dpy, owner))
}

// procName returns the short exec basename and full path from /proc, best
// effort: exe symlink preferred, /proc/<pid>/comm as a path-less fallback.
func procName(pid int) (string, string) {
	dir := "/proc/" + strconv.Itoa(pid)
	if exe, err := os.Readlink(dir + "/exe"); err == nil {
		exe = strings.TrimSuffix(exe, " (deleted)")
		return filepath.Base(exe), exe
	}
	if b, err := os.ReadFile(dir + "/comm"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, ""
		}
	}
	return "", ""
}
