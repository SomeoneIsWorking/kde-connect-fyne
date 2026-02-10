package ui

import (
	"fmt"
	"net"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"github.com/barishamil/kde-connect-fyne/internal/core"
	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

type App struct {
	FyneApp    fyne.App
	Window     fyne.Window
	Devices    *widget.List
	deviceList binding.UntypedList
	Downloads  *DownloadManager
	Engine     *core.Engine
}

func NewApp(engine *core.Engine) *App {
	a := app.NewWithID("com.barishamil.kde-connect-fyne")
	w := a.NewWindow("KDE Connect Fyne")
	w.Resize(fyne.NewSize(400, 600))

	uiApp := &App{
		FyneApp:    a,
		Window:     w,
		deviceList: binding.NewUntypedList(),
		Downloads:  NewDownloadManager(),
		Engine:     engine,
	}

	uiApp.Downloads.OnChanged = func() {
		uiApp.refreshTray()
	}

	uiApp.setupTray()
	uiApp.setupUI()
	uiApp.loadInitialDevices()
	uiApp.listenEvents()

	return uiApp
}

func (a *App) loadInitialDevices() {
	paired := a.Engine.GetPairedDevices()
	for _, info := range paired {
		// Create a DiscoveredDevice using last known IP for paired devices
		ip := net.ParseIP(info.LastIP)
		if ip == nil {
			ip = net.IPv4zero
		}
		dev := core.DiscoveredDevice{
			Identity: info.Identity,
			Addr:     &net.UDPAddr{IP: ip, Port: info.LastPort},
		}
		a.deviceList.Append(dev)

		// ALSO: Add to Engine's discoveredDevices if it has a valid IP
		if !ip.IsUnspecified() {
			a.Engine.AddDeviceManual(info.Identity, info.LastIP, info.LastPort)
		}
	}
}

func (a *App) listenEvents() {
	a.Engine.Events.On("device_discovered", func(data interface{}) {
		dev := data.(core.DiscoveredDevice)
		fyne.Do(func() {
			// Check for duplicates
			items, _ := a.deviceList.Get()
			for i, item := range items {
				if existingDev, ok := item.(core.DiscoveredDevice); ok {
					if existingDev.Identity.DeviceId == dev.Identity.DeviceId {
						// Already in list, update it if IP or Name changed
						if existingDev.Addr.IP.String() != dev.Addr.IP.String() || existingDev.Identity.DeviceName != dev.Identity.DeviceName {
							a.deviceList.SetValue(i, dev)
						}
						return
					}
				}
			}
			a.deviceList.Append(dev)
		})
	})

	a.Engine.Events.On("pair_request", func(data interface{}) {
		pairReq := data.(core.PairRequest)
		if a.Engine.IsPaired(pairReq.Identity.DeviceId) {
			a.Engine.AcceptPair(pairReq.RemoteIP)
			return
		}
		fyne.Do(func() {
			a.HandlePairRequest(pairReq)
		})
	})

	a.Engine.Events.On("pairing_changed", func(data interface{}) {
		fyne.Do(func() {
			a.Devices.Refresh()
		})
	})
}

func (a *App) refreshTray() {
	fyne.Do(func() {
		if desk, ok := a.FyneApp.Driver().(desktop.App); ok {
			activeCount := a.Downloads.GetActiveCount()

			title := "KDE Connect"
			if activeCount > 0 {
				title = fmt.Sprintf("KDE Connect (%d downloading)", activeCount)
			}

			menu := fyne.NewMenu(title,
				fyne.NewMenuItem("Show", func() {
					a.Window.Show()
				}),
			)

			recent := a.Downloads.GetRecent(5)
			if len(recent) > 0 {
				menu.Items = append(menu.Items, fyne.NewMenuItemSeparator())
				for _, d := range recent {
					p, _ := d.Progress.Get()
					s, _ := d.Status.Get()
					itemTitle := fmt.Sprintf("%s (%.0f%%) - %s", d.Name, p*100, s)
					menu.Items = append(menu.Items, fyne.NewMenuItem(itemTitle, nil))
				}
			}

			menu.Items = append(menu.Items, fyne.NewMenuItemSeparator())
			menu.Items = append(menu.Items, fyne.NewMenuItem("Quit", func() {
				a.FyneApp.Quit()
			}))

			desk.SetSystemTrayMenu(menu)
		}
	})
}

func (a *App) setupTray() {
	a.refreshTray()
}

func (a *App) setupUI() {
	a.Devices = widget.NewListWithData(
		a.deviceList,
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("Device Name"),
				widget.NewButton("Pair", func() {}),
				widget.NewButton("Unpair", func() {}),
				widget.NewButton("Files", func() {}),
			)
		},
		func(item binding.DataItem, obj fyne.CanvasObject) {
			b := item.(binding.Untyped)
			val, _ := b.Get()
			dev := val.(core.DiscoveredDevice)
			device := dev.Identity

			box := obj.(*fyne.Container)
			label := box.Objects[0].(*widget.Label)
			pairBtn := box.Objects[1].(*widget.Button)
			unpairBtn := box.Objects[2].(*widget.Button)
			filesBtn := box.Objects[3].(*widget.Button)

			name := device.DeviceName
			if name == "" {
				name = "Device " + device.DeviceId
			}
			label.SetText(name)

			if a.Engine.IsPaired(device.DeviceId) {
				pairBtn.SetText("Paired")
				pairBtn.Hide()
				unpairBtn.Show()
				filesBtn.Enable()
			} else {
				pairBtn.SetText("Pair")
				pairBtn.Show()
				unpairBtn.Hide()
				filesBtn.Disable()
			}

			pairBtn.OnTapped = func() {
				a.pairDevice(dev)
			}
			unpairBtn.OnTapped = func() {
				a.unpairDevice(dev)
			}
			filesBtn.OnTapped = func() {
				a.openFileBrowser(device)
			}
		},
	)

	a.Window.SetContent(container.NewBorder(
		widget.NewLabel("Discovered Devices"),
		nil, nil, nil,
		a.Devices,
	))
}

func (a *App) pairDevice(device core.DiscoveredDevice) {
	fmt.Printf("Pairing with %s at %s...\n", device.Identity.DeviceName, device.Addr.IP)

	go func() {
		err := a.Engine.Pair(device.Identity.DeviceId)
		fyne.Do(func() {
			if err != nil {
				fmt.Printf("Pair error: %v\n", err)
				dialog.ShowError(err, a.Window)
				return
			}
			dialog.ShowInformation("Pairing", "Pairing request sent to "+device.Identity.DeviceName, a.Window)
		})
	}()
}

func (a *App) unpairDevice(device core.DiscoveredDevice) {
	dialog.ShowConfirm("Unpair", "Are you sure you want to unpair "+device.Identity.DeviceName+"?", func(ok bool) {
		if ok {
			err := a.Engine.Unpair(device.Identity.DeviceId)
			if err != nil {
				dialog.ShowError(err, a.Window)
				return
			}

			// If device is not actively discovered, remove it from the list
			if !a.Engine.IsDiscovered(device.Identity.DeviceId) {
				items, _ := a.deviceList.Get()
				for _, item := range items {
					if d, ok := item.(core.DiscoveredDevice); ok && d.Identity.DeviceId == device.Identity.DeviceId {
						// There is no easy "RemoveAt" in binding.List, we have to Remove by value
						a.deviceList.Remove(item)
						break
					}
				}
			}
			a.Devices.Refresh()
		}
	}, a.Window)
}

func (a *App) HandlePairRequest(req core.PairRequest) {
	deviceName := req.Identity.DeviceName
	if deviceName == "" {
		deviceName = "Unknown Device"
	}

	msg := fmt.Sprintf("Allow pairing with %s?\nValidation Key: %s", deviceName, req.VerificationKey)

	// Assuming we are already in the main thread here if called via fyne.Do in listenEvents
	dialog.ShowConfirm("Pairing Request", msg, func(ok bool) {
		if ok {
			fmt.Println("Pairing accepted")
			a.Engine.AcceptPair(req.RemoteIP)
			a.Engine.MarkAsPaired(req.Identity.DeviceId)
			a.Devices.Refresh()
		} else {
			fmt.Println("Pairing rejected")
		}
	}, a.Window)
}

func (a *App) openFileBrowser(device protocol.IdentityBody) {
	fmt.Printf("Opening file browser for %s...\n", device.DeviceName)

	go func() {
		client, err := a.Engine.ConnectSFTP(device.DeviceId)
		offer, _ := a.Engine.GetSftpOffer(device.DeviceId)

		fyne.Do(func() {
			if err != nil {
				fmt.Printf("Failed to connect SFTP: %v\n", err)
				dialog.ShowError(fmt.Errorf("failed to connect SFTP: %w", err), a.Window)
				return
			}

			fb := NewFileBrowser(a, client, offer.Path)
			fb.Window.Show()
		})
	}()
}

func (a *App) Run() {
	a.Window.ShowAndRun()
}
