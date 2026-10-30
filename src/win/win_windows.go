// Package win wraps the Win32 pieces the app needs: the tray icon and
// minimize/close-to-tray behaviour, the window OBS captures, file and folder
// dialogs, message boxes, the clipboard and a single running copy.
package win

import (
	"syscall"
	"twitch_easy_media_alerts/src/appinfo"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmSetIcon       = 0x0080
	wmSysCommand    = 0x0112
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmDpiChanged    = 0x02E0
	wmTrayCallback  = 0x8000 + 1 // WM_APP itself is taken by webview's dispatch queue

	scMinimize    = 0xF020
	sizeMinimized = 1

	swHide    = 0
	swShow    = 5
	swRestore = 9

	wsExToolWindow = 0x80

	swpNoSize       = 0x0001
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020

	smCxScreen = 0
	smCyScreen = 1
	smCxIcon   = 11
	smCyIcon   = 12
	smCxSmIcon = 49
	smCySmIcon = 50

	imageIcon = 1
	iconSmall = 0
	iconBig   = 1

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifShowTip = 0x80

	mfString    = 0x0
	mfGrayed    = 0x1
	mfSeparator = 0x800

	tpmRightButton = 0x2
	tpmBottomAlign = 0x20
	tpmNoNotify    = 0x80
	tpmReturnCmd   = 0x100

	whCBT         = 5
	hcbtCreateWnd = 3

	ofnHideReadOnly    = 0x4
	ofnNoChangeDir     = 0x8
	ofnPathMustExist   = 0x800
	ofnFileMustExist   = 0x1000
	ofnExplorer        = 0x80000
	ofnDontAddToRecent = 0x2000000

	asfwAny = 0xFFFFFFFF

	AppIconID = 1 // RT_GROUP_ICON #1 from winres.json
)

// Negative window-long indexes, as variables so they convert to uintptr.
var (
	gwlpWndProc = -4
	gwlStyle    = -16
	gwlExStyle  = -20
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procFindWindowW              = user32.NewProc("FindWindowW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSetWindowsHookExW        = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx      = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx           = user32.NewProc("CallNextHookEx")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procGetOpenFileNameW         = comdlg32.NewProc("GetOpenFileNameW")

	msgTaskbarCreated = registerMessage("TaskbarCreated")
	msgShowInstance   = registerMessage(appinfo.ID + ".ShowWindow")
)

const windowClassName = "webview" // registered by go-webview2

type point struct{ x, y int32 }

type rect struct{ left, top, right, bottom int32 }

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

type createStruct struct {
	createParams uintptr
	instance     uintptr
	menu         uintptr
	parent       uintptr
	cy, cx, y, x int32
	style        int32
	name         *uint16
	class        *uint16
	exStyle      uint32
}

type cbtCreateWnd struct {
	cs          *createStruct
	insertAfter uintptr
}

type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

func registerMessage(name string) uintptr {
	p, _ := windows.UTF16PtrFromString(name)
	r, _, _ := syscall.SyscallN(procRegisterWindowMessageW.Addr(), uintptr(unsafe.Pointer(p)))
	return r
}

func utf16z(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func systemMetric(index uintptr) int32 {
	r, _, _ := syscall.SyscallN(procGetSystemMetrics.Addr(), index)
	return int32(r)
}

// SystemDPI returns the system DPI (96 = 100%).
func SystemDPI() int {
	if procGetDpiForSystem.Find() != nil {
		return 96
	}
	r, _, _ := syscall.SyscallN(procGetDpiForSystem.Addr())
	if r == 0 {
		return 96
	}
	return int(r)
}

func windowClass(hwnd uintptr) string {
	var buf [64]uint16
	n, _, _ := syscall.SyscallN(procGetClassNameW.Addr(), hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func loadAppIcon(size int32) uintptr {
	var inst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &inst)
	r, _, _ := syscall.SyscallN(procLoadImageW.Addr(), uintptr(inst), AppIconID, imageIcon, uintptr(size), uintptr(size), 0)
	return r
}

// SingleInstance returns false if another copy is already running; in that
// case it asks that copy to show its window.
func SingleInstance() bool {
	_, err := windows.CreateMutex(nil, false, utf16z(`Local\`+appinfo.ID+".SingleInstance"))
	if err != windows.ERROR_ALREADY_EXISTS {
		return true // the handle stays open for the life of the process
	}
	hwnd, _, _ := syscall.SyscallN(procFindWindowW.Addr(),
		uintptr(unsafe.Pointer(utf16z(windowClassName))), uintptr(unsafe.Pointer(utf16z(appinfo.Title))))
	if hwnd != 0 {
		syscall.SyscallN(procAllowSetForegroundWindow.Addr(), asfwAny)
		syscall.SyscallN(procPostMessageW.Addr(), hwnd, msgShowInstance, 0, 0)
	}
	return false
}

// hookWindowCreation lets adjust change how top-level "webview" windows are
// created on this thread (go-webview2 gives no say in that). Call the
// returned func once the window exists.
func hookWindowCreation(adjust func(cs *createStruct)) (unhook func()) {
	cb := syscall.NewCallback(func(code, wParam, lParam uintptr) uintptr {
		if int32(code) == hcbtCreateWnd && windowClass(wParam) == windowClassName {
			if cs := (*cbtCreateWnd)(unsafe.Pointer(lParam)).cs; cs.parent == 0 {
				adjust(cs)
			}
		}
		r, _, _ := syscall.SyscallN(procCallNextHookEx.Addr(), 0, code, wParam, lParam)
		return r
	})
	hook, _, _ := syscall.SyscallN(procSetWindowsHookExW.Addr(), whCBT, cb, 0, uintptr(windows.GetCurrentThreadId()))
	return func() {
		if hook != 0 {
			syscall.SyscallN(procUnhookWindowsHookEx.Addr(), hook)
		}
	}
}

// HideWindowCreation makes the next window start off-screen and without a
// taskbar button, so starting minimized to the tray doesn't flash a window.
func HideWindowCreation() (unhook func()) {
	return hookWindowCreation(func(cs *createStruct) {
		cs.x, cs.y = -32000, -32000
		cs.exStyle |= wsExToolWindow
	})
}

// PlaceWindowCreation makes the next window start at x, y.
func PlaceWindowCreation(x, y int) (unhook func()) {
	return hookWindowCreation(func(cs *createStruct) {
		cs.x, cs.y = int32(x), int32(y)
	})
}
