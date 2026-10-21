package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Socket owns only the socket inode it created, not the runtime directory.
type Socket struct {
	*net.UnixListener
	path string
	info os.FileInfo
	once sync.Once
	err  error
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("runtime directory must be a real directory owned by this user with mode 0700: %s", path)
	}
	return nil
}

// Listen requires an explicit valid XDG runtime directory. Existing paths are
// never unlinked to make startup succeed, including stale sockets.
func Listen(runtimeDir string) (*Socket, error) {
	if !filepath.IsAbs(runtimeDir) {
		return nil, errors.New("XDG_RUNTIME_DIR must be an absolute path")
	}
	if err := privateDirectory(runtimeDir); err != nil {
		return nil, err
	}
	dir := filepath.Join(runtimeDir, "dfmpd")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "control.sock")
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("socket path already exists; refusing to remove it: %s", path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return nil, err
	}
	s := &Socket{UnixListener: listener, path: path, info: info}
	if err := os.Chmod(path, 0600); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Socket) Path() string { return s.path }
func (s *Socket) Close() error {
	s.once.Do(func() {
		err := s.UnixListener.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			s.err = err
		}
		info, err := os.Lstat(s.path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			s.err = errors.Join(s.err, err)
			return
		}
		if !os.SameFile(s.info, info) {
			s.err = errors.Join(s.err, errors.New("socket path was replaced; refusing to remove it"))
			return
		}
		s.err = errors.Join(s.err, os.Remove(s.path))
	})
	return s.err
}
