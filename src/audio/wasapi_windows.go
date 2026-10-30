// Package audio plays sound files on a chosen output device through WASAPI,
// one at a time, decoding them with beep (mp3, wav, ogg, flac).
package audio

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/win"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal WASAPI bindings. Vtable indexes follow the declaration order in
// mmdeviceapi.h / audioclient.h (0..2 are IUnknown).
const (
	vtRelease = 2

	vtEnumAudioEndpoints      = 3
	vtGetDefaultAudioEndpoint = 4
	vtGetDevice               = 5

	vtCollectionGetCount = 3
	vtCollectionItem     = 4

	vtDeviceActivate          = 3
	vtDeviceOpenPropertyStore = 4
	vtDeviceGetID             = 5
	vtDeviceGetState          = 6

	vtPropStoreGetValue = 5

	vtClientInitialize        = 3
	vtClientGetBufferSize     = 4
	vtClientGetCurrentPadding = 6
	vtClientStart             = 10
	vtClientStop              = 11
	vtClientGetService        = 14

	vtRenderGetBuffer     = 3
	vtRenderReleaseBuffer = 4
)

const (
	eRender             = 0
	eConsole            = 0
	deviceStateActive   = 1
	clsctxAll           = 0x17
	stgmRead            = 0
	vtLPWSTR            = 31
	waveFormatIEEEFloat = 3

	audclntShareModeShared        = 0
	audclntStreamFlagsAutoConvert = 0x80000000
	audclntStreamFlagsSRCDefaultQ = 0x08000000
	bufferDuration100ns           = 2_000_000 // 200 ms
	coinitMultithreaded           = 0x0
	rpcEChangedMode               = 0x80010106
	audclntEDeviceInvalidated     = 0x88890004
	audclntEUnsupportedFormat     = 0x88890008
	hresultElementNotFound        = 0x80070490
	drainTimeout                  = 3 * time.Second
	pollInterval                  = 10 * time.Millisecond
	framesPerChunk                = 1024
)

var (
	modole32             = windows.NewLazySystemDLL("ole32.dll")
	procPropVariantClear = modole32.NewProc("PropVariantClear")

	clsidMMDeviceEnumerator = win.MustGUID("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidIMMDeviceEnumerator  = win.MustGUID("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidIAudioClient         = win.MustGUID("{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}")
	iidIAudioRenderClient   = win.MustGUID("{F294ACFC-3146-4483-A7BF-ADDCA7C260E2}")

	pkeyDeviceFriendlyName = propertyKey{fmtid: win.MustGUID("{A45C254E-DF1C-4EFD-8020-67D146A850E0}"), pid: 14}
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

type propVariant struct {
	vt       uint16
	_        [3]uint16
	val      uintptr
	reserved uintptr
}

type waveFormatEx struct {
	formatTag      uint16
	channels       uint16
	samplesPerSec  uint32
	avgBytesPerSec uint32
	blockAlign     uint16
	bitsPerSample  uint16
	cbSize         uint16
}

type hresultError uint32

func (e hresultError) Error() string {
	switch uint32(e) {
	case audclntEDeviceInvalidated:
		return i18n.L("the output device is unplugged or unavailable", "устройство вывода отключено или недоступно")
	case audclntEUnsupportedFormat:
		return i18n.L("the device doesn't support the sound format", "устройство не поддерживает формат звука")
	case hresultElementNotFound:
		return i18n.L("output device not found", "устройство вывода не найдено")
	}
	return fmt.Sprintf(i18n.L("WASAPI error 0x%08X", "ошибка WASAPI 0x%08X"), uint32(e))
}

func hr(r uintptr) error {
	if int32(r) < 0 {
		return hresultError(uint32(r))
	}
	return nil
}

// withCOM runs f on a dedicated OS thread initialised for the MTA, so callers
// on the (STA) UI thread are safe too.
func withCOM(f func() error) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := windows.CoInitializeEx(0, coinitMultithreaded)
		switch {
		case err == nil || err == windows.Errno(1): // S_OK, S_FALSE
			defer windows.CoUninitialize()
		case err == windows.Errno(rpcEChangedMode):
			// COM already set up differently on this thread; usable as is.
		default:
			result <- fmt.Errorf("CoInitializeEx: %w", err)
			return
		}
		result <- f()
	}()
	return <-result
}

func createEnumerator() (uintptr, error) {
	enum, r := win.CreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator, clsctxAll)
	if err := hr(r); err != nil {
		return 0, fmt.Errorf(i18n.L("couldn't list the audio devices: %w", "не удалось открыть список аудиоустройств: %w"), err)
	}
	return enum, nil
}

func deviceID(dev uintptr) (string, error) {
	var p *uint16
	r, _, _ := syscall.SyscallN(win.Method(dev, vtDeviceGetID), dev, uintptr(unsafe.Pointer(&p)))
	if err := hr(r); err != nil {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p), nil
}

func deviceName(dev uintptr) string {
	var store uintptr
	r, _, _ := syscall.SyscallN(win.Method(dev, vtDeviceOpenPropertyStore), dev, stgmRead, uintptr(unsafe.Pointer(&store)))
	if hr(r) != nil {
		return ""
	}
	defer win.Release(store)
	var pv propVariant
	r, _, _ = syscall.SyscallN(win.Method(store, vtPropStoreGetValue), store,
		uintptr(unsafe.Pointer(&pkeyDeviceFriendlyName)), uintptr(unsafe.Pointer(&pv)))
	if hr(r) != nil {
		return ""
	}
	defer syscall.SyscallN(procPropVariantClear.Addr(), uintptr(unsafe.Pointer(&pv)))
	if pv.vt != vtLPWSTR || pv.val == 0 {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(pv.val)))
}

func defaultDevice(enum uintptr) (uintptr, error) {
	var dev uintptr
	r, _, _ := syscall.SyscallN(win.Method(enum, vtGetDefaultAudioEndpoint), enum, eRender, eConsole, uintptr(unsafe.Pointer(&dev)))
	if err := hr(r); err != nil {
		return 0, fmt.Errorf(i18n.L("there's no default output device: %w", "нет устройства вывода по умолчанию: %w"), err)
	}
	return dev, nil
}

// deviceByID returns the active endpoint with the given ID.
func deviceByID(enum uintptr, id string) (uintptr, error) {
	idp, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return 0, err
	}
	var dev uintptr
	r, _, _ := syscall.SyscallN(win.Method(enum, vtGetDevice), enum, uintptr(unsafe.Pointer(idp)), uintptr(unsafe.Pointer(&dev)))
	if err := hr(r); err != nil {
		return 0, err
	}
	var state uint32
	r, _, _ = syscall.SyscallN(win.Method(dev, vtDeviceGetState), dev, uintptr(unsafe.Pointer(&state)))
	if hr(r) != nil || state != deviceStateActive {
		win.Release(dev)
		return 0, hresultError(audclntEDeviceInvalidated)
	}
	return dev, nil
}

type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func ListOutputDevices() ([]Device, error) {
	var devices []Device
	err := withCOM(func() error {
		enum, err := createEnumerator()
		if err != nil {
			return err
		}
		defer win.Release(enum)

		defaultID := ""
		if dev, err := defaultDevice(enum); err == nil {
			defaultID, _ = deviceID(dev)
			win.Release(dev)
		}

		var coll uintptr
		r, _, _ := syscall.SyscallN(win.Method(enum, vtEnumAudioEndpoints), enum, eRender, deviceStateActive, uintptr(unsafe.Pointer(&coll)))
		if err := hr(r); err != nil {
			return err
		}
		defer win.Release(coll)
		var count uint32
		syscall.SyscallN(win.Method(coll, vtCollectionGetCount), coll, uintptr(unsafe.Pointer(&count)))
		for i := uint32(0); i < count; i++ {
			var dev uintptr
			r, _, _ := syscall.SyscallN(win.Method(coll, vtCollectionItem), coll, uintptr(i), uintptr(unsafe.Pointer(&dev)))
			if hr(r) != nil {
				continue
			}
			id, err := deviceID(dev)
			name := deviceName(dev)
			win.Release(dev)
			if err != nil {
				continue
			}
			if name == "" {
				name = id
			}
			devices = append(devices, Device{ID: id, Name: name, Default: id == defaultID})
		}
		return nil
	})
	return devices, err
}
