//go:build windows

package syncfile

import (
	"os"
	"syscall"
)

// Windows has no device and inode: a file is known by its volume's serial
// number and its file index, as GetFileInformationByHandle gives them, which
// takes an open handle. A file is opened sharing read, write and delete, so
// that the writer may still rename or remove it while it is read.

// statID returns the identity and size of the regular file at path.
func statID(path string) (fileID, int64, error) {
	f, err := openRead(path)
	if err != nil {
		return fileID{}, 0, err
	}
	defer f.Close()
	return fileIDOf(f)
}

// fileIDOf returns the identity and size of an open file.
func fileIDOf(f *os.File) (fileID, int64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return fileID{}, 0, err
	}
	if d.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fileID{}, 0, errNotRegular
	}
	return fileID{uint64(d.VolumeSerialNumber), uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)},
		int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow), nil
}

// openRead opens path to be read, sharing it for reads, writes, renames and
// removal.
func openRead(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
