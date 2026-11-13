//go:build !windows

package workspace

import "syscall"

// diskSpace reports the bytes available to the calling user and the total
// bytes of the volume holding path. ok is false when the platform could not
// answer; callers then see zero values (SPEC-03 §5.10).
func diskSpace(path string) (free, total int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	blockSize := int64(st.Bsize)
	return int64(st.Bavail) * blockSize, int64(st.Blocks) * blockSize, true
}
