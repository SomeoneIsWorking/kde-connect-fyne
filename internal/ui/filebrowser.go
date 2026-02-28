package ui

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
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

	loadingOverlay *fyne.Container
	cancelRefresh  chan struct{}

	sortBy    string // "name", "size", "date"
	sortOrder int    // 1 for asc, -1 for desc

	lastSelectedID         widget.ListItemID
	lastSelectAt           time.Time
	lastDownloadSelectedID widget.ListItemID
	lastDownloadSelectAt   time.Time

	downloadedPaths map[string]string
}

const streamPrebufferBytes int64 = 10 * 1024 * 1024
const fileListDoubleClickThreshold = 450 * time.Millisecond

func NewFileBrowser(parent *App, client *sftp.Client, initialPath string) *FileBrowser {
	if initialPath == "" {
		initialPath = "/"
	}

	fb := &FileBrowser{
		App:                    parent,
		Client:                 client,
		path:                   initialPath,
		pathString:             binding.NewString(),
		sortBy:                 "name",
		sortOrder:              1,
		lastSelectedID:         -1,
		lastDownloadSelectedID: -1,
		downloadedPaths:        make(map[string]string),
	}
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
				widget.NewLabel("file name · size/date"),
				layout.NewSpacer(),
				widget.NewButtonWithIcon("", theme.VisibilityIcon(), func() {}),
				widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {}),
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
			infoLabel := box.Objects[1].(*widget.Label)
			actionBtn := box.Objects[3].(*widget.Button)
			revealBtn := box.Objects[4].(*widget.Button)

			// Reset thumb
			thumb.Hide()
			icon.Show()

			if f.IsDir() {
				icon.SetResource(theme.FolderIcon())
				infoLabel.SetText(fmt.Sprintf("%s · %s", f.Name(), f.ModTime().Format("2006-01-02 15:04")))
				actionBtn.SetIcon(theme.NavigateNextIcon())
				actionBtn.SetText("")
				actionBtn.Enable()
				actionBtn.OnTapped = func() {
					fb.path = path.Join(fb.path, f.Name())
					fb.pathString.Set(fb.path)
					fb.refreshFiles()
				}
				revealBtn.Disable()
				revealBtn.SetText("")
				revealBtn.SetIcon(theme.FolderOpenIcon())
				revealBtn.OnTapped = nil
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
				infoLabel.SetText(fmt.Sprintf("%s · %s · %s", f.Name(), formatSize(f.Size()), f.ModTime().Format("2006-01-02 15:04")))
				actionBtn.SetIcon(theme.VisibilityIcon())
				actionBtn.SetText("")
				actionBtn.Enable()
				actionBtn.OnTapped = func() {
					fb.openFile(f)
				}

				revealBtn.SetIcon(theme.FolderOpenIcon())
				revealBtn.SetText("")
				remotePath := path.Join(fb.path, f.Name())
				revealPath := fb.resolveDownloadedPath(remotePath, f.Name())
				if revealPath == "" {
					revealBtn.Disable()
				} else {
					revealBtn.Enable()
				}
				revealBtn.OnTapped = func() {
					revealPath := fb.resolveDownloadedPath(remotePath, f.Name())
					if revealPath == "" {
						dialog.ShowInformation("Not downloaded yet", "Preview this file first to create a local copy.", fb.App.Window)
						return
					}
					if err := revealInFileManager(revealPath); err != nil {
						dialog.ShowError(err, fb.App.Window)
					}
				}
			}

			fb.loadThumbnail(id, f, thumb, icon, box)
		},
	)

	fb.List.OnSelected = func(id widget.ListItemID) {
		now := time.Now()
		if id == fb.lastSelectedID && now.Sub(fb.lastSelectAt) <= fileListDoubleClickThreshold {
			fb.handleListDoubleClick(id)
			fb.lastSelectedID = -1
			fb.lastSelectAt = time.Time{}
		} else {
			fb.lastSelectedID = id
			fb.lastSelectAt = now
		}
		fb.List.Unselect(id)
	}

	backBtn := widget.NewButtonWithIcon("Back", theme.NavigateBackIcon(), func() {
		fb.path = path.Dir(fb.path)
		fb.pathString.Set(fb.path)
		fb.refreshFiles()
	})
	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
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
			progress := widget.NewProgressBar()
			progress.Min = 0
			progress.Max = 1
			progress.Resize(fyne.NewSize(140, progress.MinSize().Height))
			return container.NewHBox(
				widget.NewIcon(theme.DownloadIcon()),
				widget.NewLabel("filename"),
				widget.NewLabel("status"),
				progress,
				layout.NewSpacer(),
				widget.NewButtonWithIcon("", theme.VisibilityIcon(), func() {}),
				widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {}),
			)
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			item, _ := i.(binding.Untyped).Get()
			download := item.(*DownloadItem)
			box := o.(*fyne.Container)
			name := box.Objects[1].(*widget.Label)
			status := box.Objects[2].(*widget.Label)
			prog := box.Objects[3].(*widget.ProgressBar)
			preview := box.Objects[5].(*widget.Button)
			reveal := box.Objects[6].(*widget.Button)

			name.SetText(download.Name)
			status.Bind(download.Status)
			prog.Bind(download.Progress)
			preview.SetText("")
			preview.SetIcon(theme.VisibilityIcon())
			reveal.SetText("")
			reveal.SetIcon(theme.FolderOpenIcon())

			localPath := fb.resolveDownloadedPath("", download.Name)
			if localPath == "" {
				localPath, _ = download.LocalPath.Get()
			}
			if localPath == "" {
				preview.Disable()
				reveal.Disable()
			} else {
				if fb.isPreviewableName(download.Name) {
					preview.Enable()
				} else {
					preview.Disable()
				}
				reveal.Enable()
			}
			preview.OnTapped = func() {
				localPath, _ := download.LocalPath.Get()
				if localPath == "" {
					localPath = fb.resolveDownloadedPath("", download.Name)
				}
				if localPath == "" {
					dialog.ShowInformation("Not available", "This download has no local file path.", fb.App.Window)
					return
				}
				fb.openWithSystem(localPath)
			}
			reveal.OnTapped = func() {
				localPath, _ := download.LocalPath.Get()
				if localPath == "" {
					localPath = fb.resolveDownloadedPath("", download.Name)
				}
				if localPath == "" {
					dialog.ShowInformation("Not available", "This download has no local file path.", fb.App.Window)
					return
				}
				if err := revealInFileManager(localPath); err != nil {
					dialog.ShowError(err, fb.App.Window)
				}
			}
		},
	)

	downloadsList.OnSelected = func(id widget.ListItemID) {
		now := time.Now()
		if id == fb.lastDownloadSelectedID && now.Sub(fb.lastDownloadSelectAt) <= fileListDoubleClickThreshold {
			fb.handleDownloadDoubleClick(downloadsList, id)
			fb.lastDownloadSelectedID = -1
			fb.lastDownloadSelectAt = time.Time{}
		} else {
			fb.lastDownloadSelectedID = id
			fb.lastDownloadSelectAt = now
		}
		downloadsList.Unselect(id)
	}

	downloadsScroll := container.NewVScroll(downloadsList)
	downloadsScroll.SetMinSize(fyne.NewSize(0, 110))
	downloadsScroll.Resize(fyne.NewSize(0, 130))

	downloadsContainer := container.NewBorder(
		container.NewHBox(widget.NewLabelWithStyle("Downloads", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), layout.NewSpacer()),
		nil,
		nil,
		nil,
		downloadsScroll,
	)
	downloadsContainer.Hide()

	fb.App.Downloads.Downloads.AddListener(binding.NewDataListener(func() {
		l, _ := fb.App.Downloads.Downloads.Get()
		if len(l) > 0 {
			downloadsContainer.Show()
		} else {
			downloadsContainer.Hide()
		}
		fyne.Do(func() {
			fb.List.Refresh()
			downloadsList.Refresh()
		})
	}))

	centerSplit := container.NewVSplit(
		widget.NewCard("Remote Files", "", container.NewStack(
			fb.List,
			fb.loadingOverlay,
		)),
		downloadsContainer,
	)
	centerSplit.Offset = 0.82

	fb.Container = container.NewBorder(
		widget.NewCard("", "", container.NewVBox(
			container.NewHBox(backBtn, refreshBtn, layout.NewSpacer(), widget.NewLabel("Sort:"), sortSelect, orderSelect),
			container.NewHBox(widget.NewIcon(theme.FolderOpenIcon()), widget.NewLabelWithData(fb.pathString)),
		)),
		nil, nil, nil,
		centerSplit,
	)
}

func (fb *FileBrowser) handleListDoubleClick(id widget.ListItemID) {
	if id < 0 || id >= len(fb.files) {
		return
	}

	f := fb.files[id]
	if f.IsDir() {
		fb.path = path.Join(fb.path, f.Name())
		fb.pathString.Set(fb.path)
		fb.refreshFiles()
		return
	}

	if fb.isPreviewableMedia(f) {
		fb.openFile(f)
	}
}

func (fb *FileBrowser) handleDownloadDoubleClick(downloadsList *widget.List, id widget.ListItemID) {
	items, err := fb.App.Downloads.Downloads.Get()
	if err != nil || id < 0 || id >= len(items) {
		return
	}

	d, ok := items[id].(*DownloadItem)
	if !ok || !fb.isPreviewableName(d.Name) {
		return
	}

	localPath, _ := d.LocalPath.Get()
	if localPath == "" {
		localPath = fb.resolveDownloadedPath("", d.Name)
	}
	if localPath == "" {
		return
	}

	fb.openWithSystem(localPath)
	downloadsList.Unselect(id)
}

func (fb *FileBrowser) isPreviewableMedia(f os.FileInfo) bool {
	if f == nil || f.IsDir() {
		return false
	}
	return fb.isPreviewableName(f.Name())
}

func (fb *FileBrowser) isPreviewableName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".mp4", ".mkv", ".avi", ".mov", ".webm":
		return true
	default:
		return false
	}
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

		di := fb.App.Downloads.StartDownload(f.Name(), func(progress binding.Float) error {
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
		di.LocalPath.Set(localPath)
		fb.downloadedPaths[remotePath] = localPath
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

	localPath, di, err := fb.App.Downloads.StartPersistentDownload(f.Name(), func(localPath string, progress binding.Float) error {
		return fb.downloadFile(remotePath, localPath, f.Size(), progress)
	}, func(destPath string, err error) {
		fyne.Do(func() {
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
		dialog.ShowError(err, fb.App.Window)
		return
	}
	fb.downloadedPaths[remotePath] = localPath

	if isMP4 {
		go func() {
			fb.waitForVideoPrebuffer(localPath, f.Size(), di)
		}()
	}
}

func (fb *FileBrowser) revealActionLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reveal in Finder"
	case "windows":
		return "Show in Explorer"
	default:
		return "Reveal in Files"
	}
}

func (fb *FileBrowser) resolveDownloadedPath(remotePath, name string) string {
	if remotePath != "" {
		if localPath, ok := fb.downloadedPaths[remotePath]; ok {
			if localPath != "" {
				if _, err := os.Stat(localPath); err == nil {
					return localPath
				}
			}
		}
	}

	items, _ := fb.App.Downloads.Downloads.Get()
	for i := len(items) - 1; i >= 0; i-- {
		d, ok := items[i].(*DownloadItem)
		if !ok || d.Name != name {
			continue
		}
		localPath, _ := d.LocalPath.Get()
		if localPath != "" {
			if _, err := os.Stat(localPath); err == nil {
				if remotePath != "" {
					fb.downloadedPaths[remotePath] = localPath
				}
				return localPath
			}
		}
	}

	if guessed := fb.defaultDownloadPath(name); guessed != "" {
		if _, err := os.Stat(guessed); err == nil {
			if remotePath != "" {
				fb.downloadedPaths[remotePath] = guessed
			}
			return guessed
		}
	}

	return ""
}

func (fb *FileBrowser) defaultDownloadPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "kde-connect", name)
}

func revealInFileManager(targetPath string) error {
	if targetPath == "" {
		return errors.New("no path to reveal")
	}

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(absPath); statErr != nil {
		return fmt.Errorf("file is not available locally yet: %w", statErr)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", absPath)
	case "windows":
		cmd = exec.Command("explorer", "/select,", filepath.Clean(absPath))
	default:
		dir := absPath
		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			dir = filepath.Dir(absPath)
		}
		cmd = exec.Command("xdg-open", dir)
	}

	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("failed to open file manager: %w (%s)", runErr, string(out))
	}

	return nil
}

func (fb *FileBrowser) waitForVideoPrebuffer(localPath string, totalSize int64, di *DownloadItem) {
	targetBytes := streamPrebufferBytes
	if totalSize > 0 && totalSize < targetBytes {
		targetBytes = totalSize
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		status, _ := di.Status.Get()
		if strings.HasPrefix(status, "Error:") {
			return
		}

		info, err := os.Stat(localPath)
		if err == nil && info.Size() >= targetBytes {
			fyne.Do(func() {
				fb.openWithSystem(localPath)
			})
			return
		}

		if status == "Completed" {
			if err == nil && info.Size() > 0 {
				fyne.Do(func() {
					fb.openWithSystem(localPath)
				})
			}
			return
		}
	}
}

func (fb *FileBrowser) openWithSystem(path string) {
	u := storage.NewFileURI(path)
	parsedURL, _ := url.Parse(u.String())
	if err := fb.App.FyneApp.OpenURL(parsedURL); err != nil {
		dialog.ShowError(fmt.Errorf("could not open file: %w", err), fb.App.Window)
	}
}
