package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"

	"github.com/pkg/sftp"
	"golang.org/x/net/webdav"
)

// SFTPFileSystem implements webdav.FileSystem by wrapping an sftp.Client
type SFTPFileSystem struct {
	client *sftp.Client
	root   string
}

func NewSFTPFileSystem(client *sftp.Client, root string) *SFTPFileSystem {
	if root == "" {
		root = "/"
	}
	return &SFTPFileSystem{client: client, root: root}
}

func (fs *SFTPFileSystem) abs(name string) string {
	return path.Join(fs.root, name)
}

func (fs *SFTPFileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return fs.client.Mkdir(fs.abs(name))
}

func (fs *SFTPFileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	absName := fs.abs(name)

	// Check if it's a directory first
	info, err := fs.client.Stat(absName)
	if err == nil && info.IsDir() {
		return &SFTPFile{client: fs.client, name: absName, isDir: true}, nil
	}

	var f *sftp.File
	if flag == os.O_RDONLY {
		f, err = fs.client.Open(absName)
	} else if flag&os.O_CREATE != 0 {
		f, err = fs.client.Create(absName)
	} else {
		f, err = fs.client.OpenFile(absName, flag)
	}

	if err != nil {
		return nil, err
	}

	return &SFTPFile{file: f, client: fs.client, name: absName}, nil
}

func (fs *SFTPFileSystem) RemoveAll(ctx context.Context, name string) error {
	absName := fs.abs(name)
	stat, err := fs.client.Stat(absName)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return fs.client.RemoveDirectory(absName)
	}
	return fs.client.Remove(absName)
}

func (fs *SFTPFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	return fs.client.Rename(fs.abs(oldName), fs.abs(newName))
}

func (fs *SFTPFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return fs.client.Stat(fs.abs(name))
}

// SFTPFile implements webdav.File
type SFTPFile struct {
	file   *sftp.File
	client *sftp.Client
	name   string
	isDir  bool
}

func (f *SFTPFile) Close() error {
	if f.isDir {
		return nil
	}
	return f.file.Close()
}

func (f *SFTPFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}
	return f.file.Read(p)
}

func (f *SFTPFile) Seek(offset int64, whence int) (int64, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}
	return f.file.Seek(offset, whence)
}

func (f *SFTPFile) Readdir(count int) ([]os.FileInfo, error) {
	return f.client.ReadDir(f.name)
}

func (f *SFTPFile) Stat() (os.FileInfo, error) {
	return f.client.Stat(f.name)
}

func (f *SFTPFile) Write(p []byte) (int, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}
	return f.file.Write(p)
}

// WebDAVServer handles the WebDAV requests
type WebDAVServer struct {
	handler *webdav.Handler
	server  *http.Server
	Port    int
}

func NewWebDAVServer(client *sftp.Client, root string) *WebDAVServer {
	fs := NewSFTPFileSystem(client, root)
	ls := webdav.NewMemLS()
	handler := &webdav.Handler{
		FileSystem: fs,
		LockSystem: ls,
		Logger: func(r *http.Request, err error) {
			if err != nil {
				fmt.Printf("WebDAV Error: %s %s: %v\n", r.Method, r.URL.Path, err)
			} else {
				fmt.Printf("WebDAV Request: %s %s\n", r.Method, r.URL.Path)
			}
		},
	}
	return &WebDAVServer{
		handler: handler,
	}
}

func (s *WebDAVServer) Start() error {
	// Listen on a random local port
	s.server = &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: s.handler,
	}

	// We need to find which port was assigned
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.Port = ln.Addr().(*net.TCPAddr).Port

	go s.server.Serve(ln)
	return nil
}

func (s *WebDAVServer) Stop() error {
	if s.server != nil {
		return s.server.Shutdown(context.Background())
	}
	return nil
}
