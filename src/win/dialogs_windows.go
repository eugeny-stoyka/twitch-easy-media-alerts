package win

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"twitch_easy_media_alerts/src/i18n"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dialogState struct {
	ofn    openFileName
	file   [32768]uint16
	filter []uint16
}

// OpenMediaDialog shows the standard "Open" dialog for files of kind sound,
// video or image. Must run on the UI thread, outside of WebView2 callbacks.
func (s *Shell) OpenMediaDialog(current, kind string) (string, bool) {
	// Window procs re-enter Go while the dialog is open, so everything the
	// dialog points at must live on the heap, not on a movable stack.
	d := &dialogState{}
	s.dialog = d
	defer func() { s.dialog = nil }()

	filter, title := i18n.L("Sounds", "Звуки")+" (*.mp3, *.wav, *.ogg, *.flac)\x00*.mp3;*.wav;*.ogg;*.oga;*.flac\x00", i18n.L("Choose a sound", "Выберите звук")
	switch kind {
	case "video":
		filter, title = i18n.L("Videos", "Видео")+" (*.mp4, *.webm, *.mov, *.mkv)\x00*.mp4;*.m4v;*.webm;*.mov;*.mkv;*.ogv\x00", i18n.L("Choose a video", "Выберите видео")
	case "image":
		filter, title = i18n.L("Animated pictures", "Анимированные картинки")+" (*.gif, *.webp, *.apng, *.png)\x00*.gif;*.webp;*.apng;*.png\x00", i18n.L("Choose a GIF", "Выберите GIF")
	}
	d.filter = utf16.Encode([]rune(filter + i18n.L("All files", "Все файлы") + " (*.*)\x00*.*\x00\x00"))
	var initialDir *uint16
	if current != "" {
		if st, err := os.Stat(filepath.Dir(current)); err == nil && st.IsDir() {
			initialDir = utf16z(filepath.Dir(current))
		}
	}
	d.ofn = openFileName{
		owner:      s.hwnd,
		filter:     &d.filter[0],
		file:       &d.file[0],
		maxFile:    uint32(len(d.file)),
		initialDir: initialDir,
		title:      utf16z(title),
		flags:      ofnExplorer | ofnFileMustExist | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir | ofnDontAddToRecent,
	}
	d.ofn.structSize = uint32(unsafe.Sizeof(d.ofn))
	r, _, _ := syscall.SyscallN(procGetOpenFileNameW.Addr(), uintptr(unsafe.Pointer(&d.ofn)))
	if r == 0 {
		return "", false
	}
	return windows.UTF16ToString(d.file[:]), true
}

var procMessageBoxW = user32.NewProc("MessageBoxW")

func MessageBox(title, text string) {
	const mbIconError = 0x10
	syscall.SyscallN(procMessageBoxW.Addr(), 0, uintptr(unsafe.Pointer(utf16z(text))), uintptr(unsafe.Pointer(utf16z(title))), mbIconError)
}

// Folder picker: the shell's IFileOpenDialog in folder mode.
var (
	clsidFileOpenDialog             = MustGUID("{DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}")
	iidIFileOpenDialog              = MustGUID("{D57C7288-D4AD-4768-BE02-9D969532D960}")
	iidIShellItem                   = MustGUID("{43826D1E-E718-42EE-BC55-A1E261C37BFE}")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
)

const (
	vtDialogShow       = 3
	vtDialogSetOptions = 9
	vtDialogGetOptions = 10
	vtDialogSetFolder  = 12
	vtDialogSetTitle   = 17
	vtDialogGetResult  = 20
	vtItemDisplayName  = 5

	clsctxInprocServer = 0x1
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	sigdnFileSysPath   = 0x80058000
)

// PickFolder shows the standard folder picker starting at current. UI thread
// only (it uses the thread's COM apartment), outside of WebView2 callbacks.
func (s *Shell) PickFolder(title, current string) (string, bool) {
	dlg, r := CreateInstance(&clsidFileOpenDialog, &iidIFileOpenDialog, clsctxInprocServer)
	if failed(r) {
		return "", false
	}
	defer Release(dlg)
	var opts uint32
	syscall.SyscallN(Method(dlg, vtDialogGetOptions), dlg, uintptr(unsafe.Pointer(&opts)))
	syscall.SyscallN(Method(dlg, vtDialogSetOptions), dlg, uintptr(opts|fosPickFolders|fosForceFileSystem))
	syscall.SyscallN(Method(dlg, vtDialogSetTitle), dlg, uintptr(unsafe.Pointer(utf16z(title))))
	if current != "" {
		var folder uintptr
		r, _, _ := syscall.SyscallN(procSHCreateItemFromParsingName.Addr(), uintptr(unsafe.Pointer(utf16z(current))), 0,
			uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&folder)))
		if !failed(r) {
			syscall.SyscallN(Method(dlg, vtDialogSetFolder), dlg, folder)
			Release(folder)
		}
	}
	if r, _, _ := syscall.SyscallN(Method(dlg, vtDialogShow), dlg, s.hwnd); failed(r) {
		return "", false // cancelled
	}
	var item uintptr
	if r, _, _ := syscall.SyscallN(Method(dlg, vtDialogGetResult), dlg, uintptr(unsafe.Pointer(&item))); failed(r) {
		return "", false
	}
	defer Release(item)
	var p *uint16
	if r, _, _ := syscall.SyscallN(Method(item, vtItemDisplayName), item, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))); failed(r) {
		return "", false
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p), true
}

// OpenFolder shows a folder in Explorer.
func OpenFolder(dir string) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	_ = windows.ShellExecute(0, utf16z("open"), utf16z(dir), nil, nil, windows.SW_SHOWNORMAL)
}

// Ask shows a Yes/No question over the main window.
func (s *Shell) Ask(title, text string) bool {
	const mbYesNo, mbIconQuestion, idYes = 0x4, 0x20, 6
	r, _, _ := syscall.SyscallN(procMessageBoxW.Addr(), s.hwnd, uintptr(unsafe.Pointer(utf16z(text))), uintptr(unsafe.Pointer(utf16z(title))), mbYesNo|mbIconQuestion)
	return r == idYes
}

// OpenURL opens an https link in the default browser.
func OpenURL(u string) {
	if !strings.HasPrefix(u, "https://") {
		return
	}
	_ = windows.ShellExecute(0, utf16z("open"), utf16z(u), nil, nil, windows.SW_SHOWNORMAL)
}
