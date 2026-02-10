package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2/data/binding"
)

type DownloadItem struct {
	ID       string
	Name     string
	Progress binding.Float
	Status   binding.String
}

type DownloadManager struct {
	Downloads binding.UntypedList
	OnChanged func()
}

func NewDownloadManager() *DownloadManager {
	dm := &DownloadManager{
		Downloads: binding.NewUntypedList(),
	}
	dm.Downloads.AddListener(binding.NewDataListener(func() {
		if dm.OnChanged != nil {
			dm.OnChanged()
		}
	}))
	return dm
}

func (dm *DownloadManager) Add(name string) *DownloadItem {
	item := &DownloadItem{
		ID:       fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:     name,
		Progress: binding.NewFloat(),
		Status:   binding.NewString(),
	}
	item.Status.Set("Starting...")

	// Add listener to progress/status to trigger OnChanged
	item.Progress.AddListener(binding.NewDataListener(dm.notify))
	item.Status.AddListener(binding.NewDataListener(dm.notify))

	dm.Downloads.Append(item)
	return item
}

func (dm *DownloadManager) notify() {
	if dm.OnChanged != nil {
		dm.OnChanged()
	}
}

func (dm *DownloadManager) GetActiveCount() int {
	items, _ := dm.Downloads.Get()
	count := 0
	for _, it := range items {
		d := it.(*DownloadItem)
		s, _ := d.Status.Get()
		if s == "Downloading..." {
			count++
		}
	}
	return count
}

func (dm *DownloadManager) GetRecent(count int) []*DownloadItem {
	items, _ := dm.Downloads.Get()
	var recent []*DownloadItem
	start := len(items) - count
	if start < 0 {
		start = 0
	}
	for i := len(items) - 1; i >= start; i-- {
		recent = append(recent, items[i].(*DownloadItem))
	}
	return recent
}

func (dm *DownloadManager) StartDownload(name string, task func(binding.Float) error, onDone func(error)) *DownloadItem {
	di := dm.Add(name)
	di.Status.Set("Downloading...")
	go func() {
		err := task(di.Progress)
		if err != nil {
			di.Status.Set("Error: " + err.Error())
		} else {
			di.Status.Set("Completed")
			di.Progress.Set(1.0)
		}
		if onDone != nil {
			onDone(err)
		}
	}()
	return di
}

func (dm *DownloadManager) StartPersistentDownload(name string, task func(string, binding.Float) error, onDone func(string, error)) (string, *DownloadItem, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	downloadDir := filepath.Join(home, "kde-connect")
	err = os.MkdirAll(downloadDir, 0755)
	if err != nil {
		return "", nil, err
	}

	targetPath := filepath.Join(downloadDir, name)
	// We no longer truncate the file here to support resuming.
	// The task is responsible for opening the file correctly.

	di := dm.Add(name)
	di.Status.Set("Downloading...")

	go func() {
		err := task(targetPath, di.Progress)
		if err != nil {
			di.Status.Set("Error: " + err.Error())
		} else {
			di.Status.Set("Completed")
			di.Progress.Set(1.0)
		}
		if onDone != nil {
			onDone(targetPath, err)
		}
	}()

	return targetPath, di, nil
}

func (dm *DownloadManager) StartTempDownload(name, ext string, task func(string, binding.Float) error, onDone func(string, error)) (string, *DownloadItem, error) {
	tmpFile, err := os.CreateTemp("", "kdeconnect-*"+ext)
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	di := dm.Add(name)
	di.Status.Set("Downloading...")

	go func() {
		err := task(tmpPath, di.Progress)
		if err != nil {
			di.Status.Set("Error: " + err.Error())
		} else {
			di.Status.Set("Completed")
			di.Progress.Set(1.0)
		}
		if onDone != nil {
			onDone(tmpPath, err)
		}
	}()

	return tmpPath, di, nil
}
