//go:build windows

package workspace

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// diskSpace reports the bytes available to the calling user and the total
// bytes of the volume holding path. ok is false when the platform could not
// answer; callers then see zero values (SPEC-03 §5.10).
func diskSpace(path string) (free, total int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var freeAvailable, totalBytes, totalFree int64
	r1, _, _ := syscall.SyscallN(procGetDiskFreeSpaceExW.Addr(),
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		return 0, 0, false
	}
	return freeAvailable, totalBytes, true
}
