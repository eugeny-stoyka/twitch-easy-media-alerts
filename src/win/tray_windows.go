package win

import (
	"syscall"
	"twitch_easy_media_alerts/src/appinfo"
	"unicode/utf16"
	"unsafe"
)

type MenuItem struct {
	Label     string
	Disabled  bool
	Default   bool
	Separator bool
	Action    func()
}

// Shell owns the main window's tray icon and its minimize/close behaviour.
type Shell struct {
	hwnd     uintptr
	origProc uintptr
	nid      notifyIconData
	trayOn   bool
	quitting bool

	MinimizeToTray func() bool
	CloseToTray    func() bool
	Menu           func() []MenuItem

	dialog *dialogState // keeps an open file dialog's buffers on the heap
}

var theShell *Shell

func NewShell(hwnd uintptr) *Shell {
	s := &Shell{hwnd: hwnd}
	theShell = s

	small, big := loadAppIcon(systemMetric(smCxSmIcon)), loadAppIcon(systemMetric(smCxIcon))
	if small != 0 {
		syscall.SyscallN(procSendMessageW.Addr(), hwnd, wmSetIcon, iconSmall, small)
	}
	if big != 0 {
		syscall.SyscallN(procSendMessageW.Addr(), hwnd, wmSetIcon, iconBig, big)
	}

	s.nid.cbSize = uint32(unsafe.Sizeof(s.nid))
	s.nid.hWnd = hwnd
	s.nid.uID = 1
	s.nid.uFlags = nifMessage | nifIcon | nifTip | nifShowTip
	s.nid.uCallbackMessage = wmTrayCallback
	s.nid.hIcon = small
	s.setTipText(appinfo.Title)

	s.origProc, _, _ = syscall.SyscallN(procSetWindowLongPtrW.Addr(), hwnd, uintptr(gwlpWndProc), syscall.NewCallback(shellWndProc))
	s.addTray()
	return s
}

func (s *Shell) addTray() {
	r, _, _ := syscall.SyscallN(procShellNotifyIconW.Addr(), nimAdd, uintptr(unsafe.Pointer(&s.nid)))
	s.trayOn = r != 0
}

func (s *Shell) removeTray() {
	if s.trayOn {
		syscall.SyscallN(procShellNotifyIconW.Addr(), nimDelete, uintptr(unsafe.Pointer(&s.nid)))
		s.trayOn = false
	}
}

func (s *Shell) setTipText(text string) {
	u := utf16.Encode([]rune(text))
	if len(u) > len(s.nid.szTip)-1 {
		u = u[:len(s.nid.szTip)-1]
	}
	s.nid.szTip = [128]uint16{}
	copy(s.nid.szTip[:], u)
}

// SetTip updates the tray tooltip. Must run on the UI thread.
func (s *Shell) SetTip(text string) {
	s.setTipText(text)
	if s.trayOn {
		syscall.SyscallN(procShellNotifyIconW.Addr(), nimModify, uintptr(unsafe.Pointer(&s.nid)))
	}
}

func (s *Shell) Hide() {
	syscall.SyscallN(procShowWindow.Addr(), s.hwnd, swHide)
}

func (s *Shell) Show() {
	if r, _, _ := syscall.SyscallN(procIsIconic.Addr(), s.hwnd); r != 0 {
		syscall.SyscallN(procShowWindow.Addr(), s.hwnd, swRestore)
	} else {
		syscall.SyscallN(procShowWindow.Addr(), s.hwnd, swShow)
	}
	syscall.SyscallN(procSetForegroundWindow.Addr(), s.hwnd)
}

func (s *Shell) Visible() bool {
	r, _, _ := syscall.SyscallN(procIsWindowVisible.Addr(), s.hwnd)
	return r != 0
}

// Quit closes the window for real, bypassing close-to-tray.
func (s *Shell) Quit() {
	s.quitting = true
	syscall.SyscallN(procPostMessageW.Addr(), s.hwnd, wmClose, 0, 0)
}

// FinishHiddenStart undoes HideWindowCreation's tweaks on the now hidden window.
func (s *Shell) FinishHiddenStart() {
	s.Hide()
	ex, _, _ := syscall.SyscallN(procGetWindowLongPtrW.Addr(), s.hwnd, uintptr(gwlExStyle))
	syscall.SyscallN(procSetWindowLongPtrW.Addr(), s.hwnd, uintptr(gwlExStyle), ex&^wsExToolWindow)
	var rc rect
	syscall.SyscallN(procGetWindowRect.Addr(), s.hwnd, uintptr(unsafe.Pointer(&rc)))
	x := (systemMetric(smCxScreen) - (rc.right - rc.left)) / 2
	y := (systemMetric(smCyScreen) - (rc.bottom - rc.top)) / 2
	syscall.SyscallN(procSetWindowPos.Addr(), s.hwnd, 0, uintptr(max(x, 0)), uintptr(max(y, 0)), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

func (s *Shell) showMenu() {
	items := s.Menu()
	h, _, _ := syscall.SyscallN(procCreatePopupMenu.Addr())
	if h == 0 {
		return
	}
	defer syscall.SyscallN(procDestroyMenu.Addr(), h)
	for i, it := range items {
		if it.Separator {
			syscall.SyscallN(procAppendMenuW.Addr(), h, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled {
			flags |= mfGrayed
		}
		syscall.SyscallN(procAppendMenuW.Addr(), h, flags, uintptr(i+1), uintptr(unsafe.Pointer(utf16z(it.Label))))
		if it.Default {
			syscall.SyscallN(procSetMenuDefaultItem.Addr(), h, uintptr(i+1), 0)
		}
	}
	var pt point
	syscall.SyscallN(procGetCursorPos.Addr(), uintptr(unsafe.Pointer(&pt)))
	// Foreground + WM_NULL make the menu close when clicking elsewhere (KB135788).
	syscall.SyscallN(procSetForegroundWindow.Addr(), s.hwnd)
	cmd, _, _ := syscall.SyscallN(procTrackPopupMenu.Addr(), h,
		tpmReturnCmd|tpmNoNotify|tpmRightButton|tpmBottomAlign, uintptr(pt.x), uintptr(pt.y), 0, s.hwnd, 0)
	syscall.SyscallN(procPostMessageW.Addr(), s.hwnd, wmNull, 0, 0)
	if i := int(cmd) - 1; i >= 0 && i < len(items) && items[i].Action != nil {
		items[i].Action()
	}
}

func shellWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	s := theShell
	switch msg {
	case wmSysCommand:
		if wParam&0xFFF0 == scMinimize && s.MinimizeToTray() {
			s.Hide()
			return 0
		}
	case wmSize:
		if wParam == sizeMinimized && s.MinimizeToTray() {
			r, _, _ := syscall.SyscallN(procCallWindowProcW.Addr(), s.origProc, hwnd, msg, wParam, lParam)
			s.Hide()
			return r
		}
	case wmClose:
		if !s.quitting && s.CloseToTray() {
			s.Hide()
			return 0
		}
	case wmDestroy:
		s.removeTray()
	case wmDpiChanged:
		rc := (*rect)(unsafe.Pointer(lParam))
		syscall.SyscallN(procSetWindowPos.Addr(), hwnd, 0, uintptr(rc.left), uintptr(rc.top),
			uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top), swpNoZOrder|swpNoActivate)
		return 0
	case wmTrayCallback:
		switch lParam & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk:
			s.Show()
		case wmRButtonUp:
			s.showMenu()
		}
		return 0
	}
	if msgTaskbarCreated != 0 && msg == msgTaskbarCreated {
		s.addTray() // Explorer restarted
	}
	if msgShowInstance != 0 && msg == msgShowInstance {
		s.Show()
		return 0
	}
	r, _, _ := syscall.SyscallN(procCallWindowProcW.Addr(), s.origProc, hwnd, msg, wParam, lParam)
	return r
}
