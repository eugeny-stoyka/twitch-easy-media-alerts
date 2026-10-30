package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal COM plumbing, shared with the audio package.

var procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")

const vtRelease = 2 // IUnknown::Release

// MustGUID parses a GUID literal, for package-level values.
func MustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

// Method returns the address of the idx-th vtable entry of a COM object.
func Method(obj uintptr, idx int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
}

// Release drops a reference to a COM object; 0 is ignored.
func Release(obj uintptr) {
	if obj != 0 {
		syscall.SyscallN(Method(obj, vtRelease), obj)
	}
}

// CreateInstance calls CoCreateInstance and returns the object and the HRESULT.
func CreateInstance(clsid, iid *windows.GUID, clsctx uintptr) (obj, hresult uintptr) {
	hresult, _, _ = syscall.SyscallN(procCoCreateInstance.Addr(), uintptr(unsafe.Pointer(clsid)), 0, clsctx,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	return obj, hresult
}

// failed reports whether an HRESULT is an error.
func failed(hresult uintptr) bool { return int32(hresult) < 0 }
