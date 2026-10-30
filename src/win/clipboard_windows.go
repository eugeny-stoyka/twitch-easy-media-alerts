package win

import (
	"errors"
	"fmt"
	"syscall"
	"twitch_easy_media_alerts/src/i18n"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procGlobalLock       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock")
	procGlobalUnlock     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock")
)

// ClipboardText returns the clipboard's text, or "" if it holds none.
func ClipboardText() string {
	const cfUnicodeText = 13
	if r, _, _ := syscall.SyscallN(procOpenClipboard.Addr(), 0); r == 0 {
		return ""
	}
	defer syscall.SyscallN(procCloseClipboard.Addr())
	h, _, _ := syscall.SyscallN(procGetClipboardData.Addr(), cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := syscall.SyscallN(procGlobalLock.Addr(), h)
	if p == 0 {
		return ""
	}
	defer syscall.SyscallN(procGlobalUnlock.Addr(), h)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
}

var (
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc")
	procGlobalFree       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

// SetClipboardText puts text on the clipboard.
func SetClipboardText(text string) error {
	const (
		cfUnicodeText = 13
		gmemMoveable  = 0x2
	)
	u := utf16.Encode([]rune(text + "\x00"))
	if r, _, err := syscall.SyscallN(procOpenClipboard.Addr(), 0); r == 0 {
		return fmt.Errorf(i18n.L("the clipboard is busy: %w", "буфер обмена занят: %w"), err)
	}
	defer syscall.SyscallN(procCloseClipboard.Addr())
	syscall.SyscallN(procEmptyClipboard.Addr())
	h, _, _ := syscall.SyscallN(procGlobalAlloc.Addr(), gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return errors.New(i18n.L("not enough memory for the clipboard", "не хватило памяти для буфера обмена"))
	}
	p, _, _ := syscall.SyscallN(procGlobalLock.Addr(), h)
	if p == 0 {
		syscall.SyscallN(procGlobalFree.Addr(), h)
		return errors.New(i18n.L("couldn't write to the clipboard", "не удалось записать в буфер обмена"))
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	syscall.SyscallN(procGlobalUnlock.Addr(), h)
	if r, _, err := syscall.SyscallN(procSetClipboardData.Addr(), cfUnicodeText, h); r == 0 {
		syscall.SyscallN(procGlobalFree.Addr(), h) // still ours if the call failed
		return fmt.Errorf(i18n.L("couldn't write to the clipboard: %w", "не удалось записать в буфер обмена: %w"), err)
	}
	return nil
}
