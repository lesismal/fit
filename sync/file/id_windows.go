//go:build windows

package syncfile

import (
	"os"
	"syscall"
	"time"
)

// Windows has no device and inode: a file is known by its volume's serial
// number and its file index, as GetFileInformationByHandle gives them, which
// takes an open handle. A file is opened sharing read, write and delete, so
// that the writer may still rename or remove it while it is read.

// statID returns the identity, size and modification time of the regular
// file at path.
func statID(path string) (fileID, int64, time.Time, error) {
	f, err := openRead(path)
	if err != nil {
		return fileID{}, 0, time.Time{}, err
	}
	defer f.Close()
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return fileID{}, 0, time.Time{}, err
	}
	id, size, err := fileIDOf(f)
	return id, size, time.Unix(0, d.LastWriteTime.Nanoseconds()), err
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
