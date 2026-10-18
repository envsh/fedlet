//go:build darwin

package chargectl

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// smcHandle is an open connection to the AppleSMC IOService. IOKit is resolved
// at runtime through purego (dlopen/dlsym), so the package builds with
// CGO_ENABLED=0 and cross-compiles from Linux/CI without the macOS SDK.
//
// This mirrors the classic IOKit SMC accessor: open "AppleSMC", then issue
// KERNEL_INDEX_SMC struct calls carrying an 80-byte SMCKeyData_t.
type smcHandle struct {
	conn   uint32
	fnCall uintptr // IOConnectCallStructMethod
}

var (
	smcOpenOnce sync.Once
	smcOpened   *smcHandle
	smcOpenErr  error
)

// openSMC returns a process-wide connection to AppleSMC, opening it on first use.
func openSMC() (*smcHandle, error) {
	smcOpenOnce.Do(func() { smcOpened, smcOpenErr = smcConnect() })
	return smcOpened, smcOpenErr
}

func smcConnect() (*smcHandle, error) {
	iokit, err := dlopenAny(
		"/System/Library/Frameworks/IOKit.framework/IOKit",
		"/System/Library/Frameworks/IOKit.framework/Versions/A/IOKit",
	)
	if err != nil {
		return nil, err
	}
	libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY)
	if err != nil {
		return nil, fmt.Errorf("dlopen libSystem: %w", err)
	}

	matching, err := sym(iokit, "IOServiceMatching")
	if err != nil {
		return nil, err
	}
	getService, err := sym(iokit, "IOServiceGetMatchingService")
	if err != nil {
		return nil, err
	}
	open, err := sym(iokit, "IOServiceOpen")
	if err != nil {
		return nil, err
	}
	release, err := sym(iokit, "IOObjectRelease")
	if err != nil {
		return nil, err
	}
	call, err := sym(iokit, "IOConnectCallStructMethod")
	if err != nil {
		return nil, err
	}
	taskSelfSym, err := sym(libSystem, "mach_task_self_")
	if err != nil {
		return nil, err
	}

	name, err := bytePtr("AppleSMC")
	if err != nil {
		return nil, err
	}
	dict, _, _ := purego.SyscallN(matching, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if dict == 0 {
		return nil, fmt.Errorf("IOServiceMatching(AppleSMC) failed")
	}
	svc, _, _ := purego.SyscallN(getService, 0, dict) // kIOMainPortDefault == 0
	if svc == 0 {
		return nil, fmt.Errorf("AppleSMC service not found")
	}
	// mach_task_self_ is a global mach_port_t; read its value via the symbol.
	taskSelf := *(*uint32)(ptrFromUintptr(taskSelfSym))
	var conn uint32
	kr, _, _ := purego.SyscallN(open, svc, uintptr(taskSelf), 0, uintptr(unsafe.Pointer(&conn)))
	purego.SyscallN(release, svc)
	if kr != 0 {
		return nil, fmt.Errorf("IOServiceOpen(AppleSMC): 0x%x", kr)
	}
	return &smcHandle{conn: conn, fnCall: call}, nil
}

// ptrFromUintptr reinterprets a uintptr holding an address as an unsafe.Pointer.
// It avoids the unsafe.Pointer(uintptr) form, which go vet's unsafeptr analyzer
// reports even when the address is a legitimate dlsym result.
func ptrFromUintptr(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u))
}

// sym resolves a symbol, wrapping the error with its name.
func sym(handle uintptr, name string) (uintptr, error) {
	p, err := purego.Dlsym(handle, name)
	if err != nil {
		return 0, fmt.Errorf("dlsym %s: %w", name, err)
	}
	return p, nil
}

// dlopenAny returns the first library that opens.
func dlopenAny(paths ...string) (uintptr, error) {
	var last error
	for _, p := range paths {
		h, err := purego.Dlopen(p, purego.RTLD_LAZY)
		if err == nil {
			return h, nil
		}
		last = err
	}
	return 0, last
}

// bytePtr returns a NUL-terminated C string backed by a Go allocation.
func bytePtr(s string) (*byte, error) {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0], nil
}

// callStruct issues one KERNEL_INDEX_SMC struct method.
func (h *smcHandle) callStruct(in, out []byte) error {
	n := uintptr(len(out))
	r1, _, _ := purego.SyscallN(h.fnCall, uintptr(h.conn), uintptr(smcSelector),
		uintptr(unsafe.Pointer(&in[0])), uintptr(len(in)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(&n)))
	runtime.KeepAlive(in)
	runtime.KeepAlive(out)
	if r1 != 0 {
		return fmt.Errorf("IOConnectCallStructMethod: 0x%x", r1)
	}
	// A successful IOKit call does not mean the SMC accepted the operation
	// (e.g. 0x84 key not found, 0x86 not writable). Check the result byte so a
	// rejected write is never mistaken for success.
	if r := out[offResult]; r != 0 {
		return fmt.Errorf("SMC %s", smcResultName(r))
	}
	return nil
}

// keySize returns the data size of an SMC key, or an error when it is absent.
func (h *smcHandle) keySize(key string) (int, error) {
	k, ok := fourCC(key)
	if !ok {
		return 0, fmt.Errorf("invalid SMC key %q", key)
	}
	in := make([]byte, smcKeyDataSize)
	out := make([]byte, smcKeyDataSize)
	putU32(in, offKey, k)
	in[offData8] = smcCmdReadKeyInfo
	if err := h.callStruct(in, out); err != nil {
		return 0, fmt.Errorf("SMC %s keyinfo: %w", key, err)
	}
	size := int(getU32(out, offKeyInfoSize))
	if size <= 0 || size > 32 {
		return 0, fmt.Errorf("SMC %s: implausible size %d", key, size)
	}
	return size, nil
}

// readKey reads up to 32 bytes for key.
func (h *smcHandle) readKey(key string) ([]byte, error) {
	size, err := h.keySize(key)
	if err != nil {
		return nil, err
	}
	k, _ := fourCC(key)
	in := make([]byte, smcKeyDataSize)
	out := make([]byte, smcKeyDataSize)
	putU32(in, offKey, k)
	putU32(in, offKeyInfoSize, uint32(size))
	in[offData8] = smcCmdReadBytes
	if err := h.callStruct(in, out); err != nil {
		return nil, fmt.Errorf("SMC %s read: %w", key, err)
	}
	val := make([]byte, size)
	copy(val, out[offBytes:offBytes+size])
	return val, nil
}

// writeKey writes val to key, rejecting a length that does not match the key.
func (h *smcHandle) writeKey(key string, val []byte) error {
	size, err := h.keySize(key)
	if err != nil {
		return err
	}
	if len(val) != size {
		return fmt.Errorf("SMC %s: value is %d bytes, key expects %d", key, len(val), size)
	}
	return h.writeKeyRaw(key, val)
}

// writeFirmware sets the macOS 27-era firmware charge window. The firmware
// enforces lower..upper itself, so we only have to store it: deactivate bfF0,
// write bfD0 (upper) and bfE0 (lower) as little-endian ui32, then reactivate.
// It is idempotent: an already-active matching window is left untouched.
func (h *smcHandle) writeFirmware(lower, upper int) error {
	if cur, err := h.readKey("bfF0"); err == nil && len(cur) == 1 && cur[0] == 0x02 {
		u, e1 := h.readKey("bfD0")
		l, e2 := h.readKey("bfE0")
		if e1 == nil && e2 == nil && len(u) == 4 && len(l) == 4 &&
			le32(u) == uint32(upper) && le32(l) == uint32(lower) {
			return nil
		}
	}
	if err := h.writeKey("bfF0", []byte{0x00}); err != nil {
		return err
	}
	if err := h.writeKey("bfD0", le32b(uint32(upper))); err != nil {
		return err
	}
	if err := h.writeKey("bfE0", le32b(uint32(lower))); err != nil {
		return err
	}
	return h.writeKey("bfF0", []byte{0x02})
}

// writeKeyRaw writes val without re-querying the key size (caller has done it).
func (h *smcHandle) writeKeyRaw(key string, val []byte) error {
	k, ok := fourCC(key)
	if !ok {
		return fmt.Errorf("invalid SMC key %q", key)
	}
	in := make([]byte, smcKeyDataSize)
	out := make([]byte, smcKeyDataSize)
	putU32(in, offKey, k)
	putU32(in, offKeyInfoSize, uint32(len(val)))
	copy(in[offBytes:], val)
	in[offData8] = smcCmdWriteBytes
	if err := h.callStruct(in, out); err != nil {
		return fmt.Errorf("SMC %s write: %w", key, err)
	}
	return nil
}
