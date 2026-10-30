package win

import (
	"syscall"
	"unsafe"
)

const (
	wmGetMinMaxInfo = 0x0024
	wmExitSizeMove  = 0x0232

	scMaximize       = 0xF030
	swShowNoActivate = 4

	wsCaption = 0x00C00000
	wsSysMenu = 0x00080000
	wsVisible = 0x10000000

	monitorDefaultToNull = 0
	swpNoMove            = 0x0002
)

var (
	procAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	procAdjustWindowRectEx       = user32.NewProc("AdjustWindowRectEx")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
)

type minMaxInfo struct {
	reserved, maxSize, maxPosition, minTrackSize, maxTrackSize point
}

// PointOnScreen reports whether x, y lies on some monitor.
func PointOnScreen(x, y int) bool {
	pt := uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32 // POINT by value
	r, _, _ := syscall.SyscallN(procMonitorFromPoint.Addr(), pt, monitorDefaultToNull)
	return r != 0
}

// OverlayShell manages the window OBS captures: its client area stays exactly
// the configured resolution, it can't be minimized (OBS can't capture that),
// and closing only hides it, because destroying any go-webview2 window quits
// the whole message loop.
type OverlayShell struct {
	hwnd, origProc uintptr
	width, height  int
	onClose        func()
	onMoved        func(x, y int)
}

var theOverlay *OverlayShell

func NewOverlayShell(hwnd uintptr, onClose func(), onMoved func(x, y int)) *OverlayShell {
	o := &OverlayShell{hwnd: hwnd, onClose: onClose, onMoved: onMoved}
	theOverlay = o
	style, _, _ := syscall.SyscallN(procGetWindowLongPtrW.Addr(), hwnd, uintptr(gwlStyle))
	syscall.SyscallN(procSetWindowLongPtrW.Addr(), hwnd, uintptr(gwlStyle), style&wsVisible|wsCaption|wsSysMenu)
	o.origProc, _, _ = syscall.SyscallN(procSetWindowLongPtrW.Addr(), hwnd, uintptr(gwlpWndProc), syscall.NewCallback(overlayWndProc))
	return o
}

// SetClientSize resizes the window so its client area is w x h pixels.
func (o *OverlayShell) SetClientSize(w, h int) {
	o.width, o.height = w, h
	o.resize(0, 0, false)
}

func (o *OverlayShell) resize(x, y int32, move bool) {
	style, _, _ := syscall.SyscallN(procGetWindowLongPtrW.Addr(), o.hwnd, uintptr(gwlStyle))
	ex, _, _ := syscall.SyscallN(procGetWindowLongPtrW.Addr(), o.hwnd, uintptr(gwlExStyle))
	rc := rect{0, 0, int32(o.width), int32(o.height)}
	if procAdjustWindowRectExForDpi.Find() == nil && procGetDpiForWindow.Find() == nil {
		dpi, _, _ := syscall.SyscallN(procGetDpiForWindow.Addr(), o.hwnd)
		syscall.SyscallN(procAdjustWindowRectExForDpi.Addr(), uintptr(unsafe.Pointer(&rc)), style&0xFFFFFFFF, 0, ex&0xFFFFFFFF, dpi)
	} else {
		syscall.SyscallN(procAdjustWindowRectEx.Addr(), uintptr(unsafe.Pointer(&rc)), style&0xFFFFFFFF, 0, ex&0xFFFFFFFF)
	}
	flags := uintptr(swpNoZOrder | swpNoActivate | swpFrameChanged)
	if !move {
		flags |= swpNoMove
	}
	syscall.SyscallN(procSetWindowPos.Addr(), o.hwnd, 0, uintptr(x), uintptr(y),
		uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top), flags)
}

func (o *OverlayShell) Show(activate bool) {
	if activate {
		syscall.SyscallN(procShowWindow.Addr(), o.hwnd, swShow)
		syscall.SyscallN(procSetForegroundWindow.Addr(), o.hwnd)
	} else {
		syscall.SyscallN(procShowWindow.Addr(), o.hwnd, swShowNoActivate)
	}
}

func (o *OverlayShell) Hide() {
	syscall.SyscallN(procShowWindow.Addr(), o.hwnd, swHide)
}

func overlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	o := theOverlay
	switch msg {
	case wmClose:
		if o.onClose != nil {
			o.onClose()
		}
		return 0
	case wmSysCommand:
		if cmd := wParam & 0xFFF0; cmd == scMinimize || cmd == scMaximize {
			return 0
		}
	case wmGetMinMaxInfo:
		// Let the window grow past the screen: a 1080p client area plus the
		// title bar doesn't fit on a 1080p monitor.
		r, _, _ := syscall.SyscallN(procCallWindowProcW.Addr(), o.origProc, hwnd, msg, wParam, lParam)
		(*minMaxInfo)(unsafe.Pointer(lParam)).maxTrackSize = point{32767, 32767}
		return r
	case wmDpiChanged:
		rc := (*rect)(unsafe.Pointer(lParam))
		o.resize(rc.left, rc.top, true) // keep the exact pixel size
		return 0
	case wmExitSizeMove:
		var rc rect
		syscall.SyscallN(procGetWindowRect.Addr(), hwnd, uintptr(unsafe.Pointer(&rc)))
		if o.onMoved != nil {
			o.onMoved(int(rc.left), int(rc.top))
		}
	}
	r, _, _ := syscall.SyscallN(procCallWindowProcW.Addr(), o.origProc, hwnd, msg, wParam, lParam)
	return r
}
