package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/net/webdav"
)

type cacheEntry struct {
	value     interface{}
	timestamp time.Time
}

// SFTPFileSystem implements webdav.FileSystem by wrapping an sftp.Client
type SFTPFileSystem struct {
	client *sftp.Client
	root   string
	cache  sync.Map // Path -> cacheEntry
	ttl    time.Duration
}

func NewSFTPFileSystem(client *sftp.Client, root string) *SFTPFileSystem {
	if root == "" {
		root = "/"
	}
	return &SFTPFileSystem{
		client: client,
		root:   root,
		ttl:    5 * time.Second, // Cache stats for 5 seconds
	}
}

func (fs *SFTPFileSystem) abs(name string) string {
	name = path.Clean("/" + name)

	// If the name already starts with the root path, don't double-prefix it.
	// This handles clients that might be sending absolute device paths.
	if fs.root != "/" && strings.HasPrefix(name, fs.root) {
		return name
	}

	if name == "/" {
		return fs.root
	}

	// Join root with the relative path from name
	return path.Join(fs.root, strings.TrimPrefix(name, "/"))
}

func (fs *SFTPFileSystem) getCache(path string) (interface{}, bool) {
	if val, ok := fs.cache.Load(path); ok {
		entry := val.(cacheEntry)
		if time.Since(entry.timestamp) < fs.ttl {
			return entry.value, true
		}
		fs.cache.Delete(path)
	}
	return nil, false
}

func (fs *SFTPFileSystem) setCache(path string, value interface{}) {
	fs.cache.Store(path, cacheEntry{value: value, timestamp: time.Now()})
}

func (fs *SFTPFileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	absName := fs.abs(name)
	fs.cache.Delete("stat:" + absName)
	return fs.client.Mkdir(absName)
}

func (fs *SFTPFileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	absName := fs.abs(name)

	// Check if it's a directory first
	info, err := fs.Stat(ctx, name)
	if err == nil && info.IsDir() {
		return &SFTPFile{fs: fs, client: fs.client, name: absName, isDir: true}, nil
	}

	var f *sftp.File
	if flag == os.O_RDONLY {
		f, err = fs.client.Open(absName)
	} else if flag&os.O_CREATE != 0 {
		fs.cache.Delete("stat:" + absName)
		f, err = fs.client.Create(absName)
	} else {
		f, err = fs.client.OpenFile(absName, flag)
	}

	if err != nil {
		return nil, err
	}

	return &SFTPFile{file: f, fs: fs, client: fs.client, name: absName}, nil
}

func (fs *SFTPFileSystem) RemoveAll(ctx context.Context, name string) error {
	absName := fs.abs(name)
	fs.cache.Delete("stat:" + absName)
	stat, err := fs.Stat(ctx, name)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return fs.client.RemoveDirectory(absName)
	}
	return fs.client.Remove(absName)
}

func (fs *SFTPFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	absOld := fs.abs(oldName)
	absNew := fs.abs(newName)
	fs.cache.Delete("stat:" + absOld)
	fs.cache.Delete("stat:" + absNew)
	return fs.client.Rename(absOld, absNew)
}

func (fs *SFTPFileSystem) isIgnored(name string) bool {
	base := path.Base(name)
	return strings.HasPrefix(base, "._") || base == ".DS_Store" || base == ".metadata_never_index"
}

func (fs *SFTPFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	absName := fs.abs(name)
	if val, ok := fs.getCache("stat:" + absName); ok {
		return val.(os.FileInfo), nil
	}

	info, err := fs.client.Stat(absName)
	if err != nil {
		// Some Android SFTP servers require a trailing slash for the root directory or subdirs
		if !strings.HasSuffix(absName, "/") {
			info, err = fs.client.Stat(absName + "/")
		}
		if err != nil {
			// Try Lstat if Stat fails
			info, err = fs.client.Lstat(absName)
		}
	}

	if err == nil {
		fs.setCache("stat:"+absName, info)
	} else {
		// Suppress logs for common macOS metadata files that won't exist on Android
		if !fs.isIgnored(name) {
			fmt.Printf("SFTP Stat Failed for %s (abs: %s): %v\n", name, absName, err)
		}
	}
	return info, err
}

// SFTPFile implements webdav.File
type SFTPFile struct {
	file         *sftp.File
	fs           *SFTPFileSystem
	client       *sftp.Client
	name         string
	isDir        bool
	readdirCache []os.FileInfo
	readdirIdx   int
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
	if !f.isDir {
		return nil, os.ErrInvalid
	}

	if f.readdirCache == nil {
		if val, ok := f.fs.getCache("readdir:" + f.name); ok {
			f.readdirCache = val.([]os.FileInfo)
		} else {
			infos, err := f.client.ReadDir(f.name)
			if err != nil {
				return nil, err
			}
			f.readdirCache = infos
			f.fs.setCache("readdir:"+f.name, infos)
			// Proactively cache individual stats
			for _, info := range infos {
				f.fs.setCache("stat:"+path.Join(f.name, info.Name()), info)
			}
		}
		f.readdirIdx = 0
	}

	if count <= 0 {
		return f.readdirCache, nil
	}

	if f.readdirIdx >= len(f.readdirCache) {
		return nil, io.EOF
	}

	end := f.readdirIdx + count
	if end > len(f.readdirCache) {
		end = len(f.readdirCache)
	}

	res := f.readdirCache[f.readdirIdx:end]
	f.readdirIdx = end
	return res, nil
}

func (f *SFTPFile) Stat() (os.FileInfo, error) {
	return f.fs.Stat(context.Background(), f.name)
}

func (f *SFTPFile) Write(p []byte) (int, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}
	f.fs.cache.Delete("stat:" + f.name)
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
			// Suppress logs for common macOS metadata files
			if fs.isIgnored(r.URL.Path) {
				return
			}
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
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Finder often expects some form of auth for network volumes.
			// We provide a dummy one that accepts everything.
			if _, _, ok := r.BasicAuth(); !ok {
				// We don't actually enforce it, but we can accept it.
				// If we want to force Finder to send it:
				// w.Header().Set("WWW-Authenticate", `Basic realm="KDE Connect"`)
				// w.WriteHeader(http.StatusUnauthorized)
				// return
			}
			s.handler.ServeHTTP(w, r)
		}),
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
