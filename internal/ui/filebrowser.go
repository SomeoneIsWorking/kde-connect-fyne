package ui

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/pkg/sftp"
)

type FileBrowser struct {
	App        *App
	Container  *fyne.Container
	Client     *sftp.Client
	List       *widget.List
	files      []os.FileInfo
	path       string
	pathString binding.String
	progress   *widget.ProgressBar

	loadingOverlay *fyne.Container
	cancelRefresh  chan struct{}

	sortBy    string // "name", "size", "date"
	sortOrder int    // 1 for asc, -1 for desc
}

func NewFileBrowser(parent *App, client *sftp.Client, initialPath string) *FileBrowser {
	if initialPath == "" {
		initialPath = "/"
	}

	fb := &FileBrowser{
		App:        parent,
		Client:     client,
		path:       initialPath,
		pathString: binding.NewString(),
		progress:   widget.NewProgressBar(),
		sortBy:     "name",
		sortOrder:  1,
	}
	fb.progress.Hide()
	fb.pathString.Set(fb.path)

	fb.setupUI()
	fb.refreshFiles()
	return fb
}

type progressWriter struct {
	total      int64
	downloaded int64
	onProgress func(float64)
	writer     io.Writer
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	pw.downloaded += int64(n)
	if pw.total > 0 {
		pw.onProgress(float64(pw.downloaded) / float64(pw.total))
	}
	return n, err
}

func (fb *FileBrowser) setupUI() {
	// Setup Loading Overlay
	spinner := widget.NewProgressBarInfinite()
	cancelBtn := widget.NewButton("Cancel", func() {
		if fb.cancelRefresh != nil {
			close(fb.cancelRefresh)
			fb.cancelRefresh = nil
		}
		fb.loadingOverlay.Hide()
	})
	fb.loadingOverlay = container.NewCenter(
		container.NewVBox(
			widget.NewLabel("Loading directory..."),
			spinner,
			cancelBtn,
		),
	)
	fb.loadingOverlay.Hide()

	fb.List = widget.NewList(
		func() int {
			return len(fb.files)
		},
		func() fyne.CanvasObject {
			return container.NewHBox(
				container.NewStack(
					widget.NewIcon(theme.FileIcon()),
					canvas.NewImageFromResource(theme.FileIcon()),
				),
				container.NewVBox(
					widget.NewLabel("file name"),
					widget.NewLabel("size / date"),
				),
				layout.NewSpacer(),
				widget.NewButtonWithIcon("", theme.DownloadIcon(), func() {}),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(fb.files) {
				return
			}
			f := fb.files[id]
			box := obj.(*fyne.Container)
			stack := box.Objects[0].(*fyne.Container)
			icon := stack.Objects[0].(*widget.Icon)
			thumb := stack.Objects[1].(*canvas.Image)
			infoBox := box.Objects[1].(*fyne.Container)
			nameLabel := infoBox.Objects[0].(*widget.Label)
			detailLabel := infoBox.Objects[1].(*widget.Label)
			btn := box.Objects[3].(*widget.Button)

			// Reset thumb
			thumb.Hide()
			icon.Show()

			if f.IsDir() {
				icon.SetResource(theme.FolderIcon())
				detailLabel.SetText(fmt.Sprintf("%s", f.ModTime().Format("2006-01-02 15:04")))
			} else {
				ext := strings.ToLower(filepath.Ext(f.Name()))
				switch ext {
				case ".jpg", ".jpeg", ".png", ".gif":
					icon.SetResource(theme.FileImageIcon())
				case ".mp4", ".mkv", ".avi":
					icon.SetResource(theme.FileVideoIcon())
				default:
					icon.SetResource(theme.FileIcon())
				}
				detailLabel.SetText(fmt.Sprintf("%s | %s", formatSize(f.Size()), f.ModTime().Format("2006-01-02 15:04")))
			}
			nameLabel.SetText(f.Name())
			btn.OnTapped = func() {
				fb.startDownload(f)
			}

			fb.loadThumbnail(id, f, thumb, icon, box)
		},
	)

	fb.List.OnSelected = func(id widget.ListItemID) {
		if id >= len(fb.files) {
			return
		}
		f := fb.files[id]
		if f.IsDir() {
			fb.path = path.Join(fb.path, f.Name())
			fb.pathString.Set(fb.path)
			fb.refreshFiles()
		} else {
			fb.openFile(f)
		}
	}

	backBtn := widget.NewButtonWithIcon("Back", theme.NavigateBackIcon(), func() {
		fb.path = path.Dir(fb.path)
		fb.pathString.Set(fb.path)
		fb.refreshFiles()
	})

	sortSelect := widget.NewSelect([]string{"Name", "Size", "Date"}, func(s string) {
		fb.sortBy = strings.ToLower(s)
		fb.sortFiles()
		fb.List.Refresh()
	})
	sortSelect.SetSelected("Name")

	orderSelect := widget.NewSelect([]string{"Asc", "Desc"}, func(s string) {
		if s == "Asc" {
			fb.sortOrder = 1
		} else {
			fb.sortOrder = -1
		}
		fb.sortFiles()
		fb.List.Refresh()
	})
	orderSelect.SetSelected("Asc")

	downloadsList := widget.NewListWithData(
		fb.App.Downloads.Downloads,
		func() fyne.CanvasObject {
			return container.NewVBox(
				widget.NewLabel("filename"),
				widget.NewProgressBar(),
			)
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			item, _ := i.(binding.Untyped).Get()
			download := item.(*DownloadItem)
			box := o.(*fyne.Container)
			name := box.Objects[0].(*widget.Label)
			prog := box.Objects[1].(*widget.ProgressBar)

			name.SetText(download.Name)
			prog.Bind(download.Progress)
		},
	)

	downloadsContainer := container.NewVBox(
		widget.NewSeparator(),
		widget.NewLabel("Active Downloads"),
		container.NewStack(downloadsList),
	)
	downloadsContainer.Hide()

	fb.App.Downloads.Downloads.AddListener(binding.NewDataListener(func() {
		l, _ := fb.App.Downloads.Downloads.Get()
		if len(l) > 0 {
			downloadsContainer.Show()
		} else {
			downloadsContainer.Hide()
		}
	}))

	fb.Container = container.NewBorder(
		container.NewVBox(
			container.NewHBox(backBtn, layout.NewSpacer(), widget.NewLabel("Sort:"), sortSelect, orderSelect),
			container.NewHBox(widget.NewLabel("Path: "), widget.NewLabelWithData(fb.pathString)),
			fb.progress,
		),
		downloadsContainer, nil, nil,
		container.NewStack(fb.List, fb.loadingOverlay),
	)
}

func formatSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	} else if size < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	} else if size < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(size)/(1024*1024*1024))
}

func (fb *FileBrowser) sortFiles() {
	sort.Slice(fb.files, func(i, j int) bool {
		// Always keep directories at top if sorting by name?
		// KDE Connect usually keeps dirs together. Let's do that.
		if fb.files[i].IsDir() && !fb.files[j].IsDir() {
			return true
		}
		if !fb.files[i].IsDir() && fb.files[j].IsDir() {
			return false
		}

		var less bool
		switch fb.sortBy {
		case "size":
			less = fb.files[i].Size() < fb.files[j].Size()
		case "date":
			less = fb.files[i].ModTime().Before(fb.files[j].ModTime())
		default: // name
			less = strings.ToLower(fb.files[i].Name()) < strings.ToLower(fb.files[j].Name())
		}

		if fb.sortOrder == -1 {
			return !less
		}
		return less
	})
}

func (fb *FileBrowser) refreshFiles() {
	if fb.cancelRefresh != nil {
		close(fb.cancelRefresh)
	}
	fb.cancelRefresh = make(chan struct{})
	cancel := fb.cancelRefresh

	fb.loadingOverlay.Show()

	go func() {
		files, err := fb.Client.ReadDir(fb.path)

		select {
		case <-cancel:
			return // Operation was cancelled
		default:
		}

		fyne.Do(func() {
			fb.loadingOverlay.Hide()

			if err != nil {
				fmt.Printf("Error reading dir: %v\n", err)
				// Clear files if there was an error to avoid showing old data
				fb.files = nil
				fb.List.Refresh()
				return
			}
			fb.files = files
			fb.sortFiles()
			fb.List.Refresh()
		})
	}()
}

func (fb *FileBrowser) loadThumbnail(id widget.ListItemID, f os.FileInfo, thumb *canvas.Image, icon *widget.Icon, box *fyne.Container) {
	ext := strings.ToLower(filepath.Ext(f.Name()))
	isImage := ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif"
	if !isImage || f.Size() >= 2*1024*1024 {
		return
	}

	remoteP := path.Join(fb.path, f.Name())
	go func() {
		src, err := fb.Client.Open(remoteP)
		if err != nil {
			return
		}
		data, err := io.ReadAll(src)
		src.Close()
		if err != nil {
			return
		}

		fyne.Do(func() {
			if id >= len(fb.files) || fb.files[id].Name() != f.Name() {
				return
			}
			thumb.Resource = fyne.NewStaticResource(f.Name(), data)
			thumb.FillMode = canvas.ImageFillContain
			thumb.SetMinSize(fyne.NewSize(32, 32))
			thumb.Show()
			icon.Hide()
			box.Refresh()
		})
	}()
}

func (fb *FileBrowser) startDownload(f os.FileInfo) {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}

		destPath := uri.Path()
		remotePath := path.Join(fb.path, f.Name())
		localPath := filepath.Join(destPath, f.Name())

		fb.App.Downloads.StartDownload(f.Name(), func(progress binding.Float) error {
			if f.IsDir() {
				return fb.downloadDir(remotePath, localPath, progress)
			}
			return fb.downloadFile(remotePath, localPath, f.Size(), progress)
		}, func(err error) {
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, fb.App.Window)
				} else {
					dialog.ShowInformation("Success", fmt.Sprintf("Downloaded %s to %s", f.Name(), destPath), fb.App.Window)
				}
			})
		})
	}, fb.App.Window)
	d.Show()
}

func (fb *FileBrowser) downloadFile(remotePath, localPath string, size int64, progress binding.Float) error {
	var initialOffset int64
	var dst *os.File
	var err error

	// Check if local file already exists to resume
	if info, err := os.Stat(localPath); err == nil {
		if info.Size() < size {
			fmt.Printf("Resuming download of %s from %d bytes\n", localPath, info.Size())
			dst, err = os.OpenFile(localPath, os.O_APPEND|os.O_WRONLY, 0644)
			initialOffset = info.Size()
		} else if info.Size() == size {
			fmt.Printf("File %s already fully downloaded\n", localPath)
			progress.Set(1.0)
			return nil
		} else {
			// Local file is larger? Unexpected. Just restart.
			dst, err = os.Create(localPath)
		}
	} else {
		dst, err = os.Create(localPath)
	}

	if err != nil {
		return err
	}
	defer dst.Close()

	src, err := fb.Client.Open(remotePath)
	if err != nil {
		return err
	}
	defer src.Close()

	if initialOffset > 0 {
		_, err = src.Seek(initialOffset, io.SeekStart)
		if err != nil {
			return fmt.Errorf("failed to seek remote file: %w", err)
		}
	}

	pw := &progressWriter{
		total:      size,
		downloaded: initialOffset,
		onProgress: func(p float64) {
			progress.Set(p)
		},
		writer: dst,
	}

	_, err = io.Copy(pw, src)
	return err
}

func (fb *FileBrowser) downloadDir(remotePath, localPath string, progress binding.Float) error {
	err := os.MkdirAll(localPath, 0755)
	if err != nil {
		return err
	}

	files, err := fb.Client.ReadDir(remotePath)
	if err != nil {
		return err
	}

	for _, f := range files {
		rPath := path.Join(remotePath, f.Name())
		lPath := filepath.Join(localPath, f.Name())

		if f.IsDir() {
			if err := fb.downloadDir(rPath, lPath, progress); err != nil {
				return err
			}
		} else {
			if err := fb.downloadFile(rPath, lPath, f.Size(), progress); err != nil {
				return err
			}
		}
	}
	return nil
}

func (fb *FileBrowser) openFile(f os.FileInfo) {
	remotePath := path.Join(fb.path, f.Name())
	ext := strings.ToLower(filepath.Ext(f.Name()))
	isMP4 := ext == ".mp4"

	fb.progress.Show()
	fb.progress.SetValue(0)

	localPath, di, err := fb.App.Downloads.StartPersistentDownload(f.Name(), func(localPath string, progress binding.Float) error {
		return fb.downloadFile(remotePath, localPath, f.Size(), progress)
	}, func(destPath string, err error) {
		fyne.Do(func() {
			fb.progress.Hide()
			if err != nil {
				dialog.ShowError(err, fb.App.Window)
				return
			}

			if !isMP4 {
				fb.openWithSystem(destPath)
			}
		})
	})

	if err != nil {
		fb.hideProgressError(err)
		return
	}

	if isMP4 {
		go func() {
			time.Sleep(500 * time.Millisecond)
			fyne.Do(func() {
				fb.openWithSystem(localPath)
			})
		}()
	}

	// Link browser's internal progress bar to the download item
	di.Progress.AddListener(binding.NewDataListener(func() {
		val, _ := di.Progress.Get()
		fyne.Do(func() {
			fb.progress.SetValue(val)
		})
	}))
}

func (fb *FileBrowser) openWithSystem(path string) {
	u := storage.NewFileURI(path)
	parsedURL, _ := url.Parse(u.String())
	if err := fb.App.FyneApp.OpenURL(parsedURL); err != nil {
		dialog.ShowError(fmt.Errorf("could not open file: %w", err), fb.App.Window)
	}
}

func (fb *FileBrowser) hideProgressError(err error) {
	fyne.Do(func() {
		fb.progress.Hide()
		dialog.ShowError(err, fb.App.Window)
	})
}
