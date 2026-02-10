package core

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/barishamil/kde-connect-fyne/internal/events"
	"github.com/barishamil/kde-connect-fyne/internal/network"
	"github.com/barishamil/kde-connect-fyne/internal/protocol"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type DiscoveredDevice struct {
	Identity protocol.IdentityBody
	Addr     *net.UDPAddr
}

type PairRequest struct {
	RemoteIP        string
	Identity        protocol.IdentityBody
	VerificationKey string
}

type Engine struct {
	Events            *events.EventEmitter
	Identity          protocol.IdentityBody
	Cert              *tls.Certificate
	discoveredDevices map[string]DiscoveredDevice
	pairedDevices     map[string]PairedDeviceInfo
	sftpOffers        map[string]protocol.SftpBody
	activeConns       map[string]*network.Connection
	pendingPairing    map[string]bool
	btProvider        *network.BluetoothLinkProvider
	mu                sync.RWMutex
}

func (e *Engine) AddDeviceManual(identity protocol.IdentityBody, ip string, port int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	// We don't really need UDPAddr to be perfect, just the IP for pairing
	dev := DiscoveredDevice{Identity: identity, Addr: addr}
	e.discoveredDevices[identity.DeviceId] = dev
	e.Events.Emit("device_discovered", dev)
}

func NewEngine(deviceName string) (*Engine, error) {
	engine := &Engine{
		Events:            events.NewEventEmitter(),
		discoveredDevices: make(map[string]DiscoveredDevice),
		pairedDevices:     make(map[string]PairedDeviceInfo),
		sftpOffers:        make(map[string]protocol.SftpBody),
		activeConns:       make(map[string]*network.Connection),
		pendingPairing:    make(map[string]bool),
	}

	// Try to load existing config
	if err := engine.LoadConfig(); err == nil {
		if cert, err := engine.LoadCertificate(); err == nil {
			engine.Cert = cert
			changed := false
			// Update device name if it changed
			if engine.Identity.DeviceName != deviceName {
				engine.Identity.DeviceName = deviceName
				changed = true
			}
			// Update bluetooth address if missing
			if engine.Identity.BluetoothAddress == "" {
				addr := getBluetoothAddress()
				if addr != "" {
					engine.Identity.BluetoothAddress = addr
					changed = true
				}
			}
			if changed {
				engine.SaveConfig()
			}
			engine.btProvider = network.NewBluetoothLinkProvider(engine.Identity, engine.Cert)
			return engine, nil
		}
	}

	// KDE Connect deviceId should be between 32 and 38 characters
	deviceId := fmt.Sprintf("fyne-%030x", time.Now().UnixNano())
	cert, certPEM, privPEM, err := protocol.GenerateCertificate(deviceId) // Use DeviceID as Common Name
	if err != nil {
		return nil, err
	}

	// Debug: Print Cert Fingerprint
	hash := sha256.Sum256(cert.Certificate[0])
	fmt.Printf("Engine Certificate Fingerprint: %x\n", hash)

	// Try to find an available port in the KDE Connect range
	port := 1716
	for p := 1716; p <= 1764; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			l.Close()
			port = p
			break
		}
	}

	identity := protocol.IdentityBody{
		DeviceId:             deviceId,
		DeviceName:           deviceName,
		DeviceType:           "desktop",
		ProtocolVersion:      8,
		TcpPort:              port,
		BluetoothAddress:     getBluetoothAddress(),
		IncomingCapabilities: []string{"kdeconnect.ping", "kdeconnect.identity", "kdeconnect.pair", "kdeconnect.sftp"},
		OutgoingCapabilities: []string{"kdeconnect.ping", "kdeconnect.identity", "kdeconnect.pair", "kdeconnect.sftp"},
	}

	// Deep copy cert to separate heap allocation
	eCert := new(tls.Certificate)
	eCert.PrivateKey = cert.PrivateKey
	for _, c := range cert.Certificate {
		cb := make([]byte, len(c))
		copy(cb, c)
		eCert.Certificate = append(eCert.Certificate, cb)
	}

	engine.Identity = identity
	engine.Cert = eCert
	engine.btProvider = network.NewBluetoothLinkProvider(identity, eCert)

	// Save new config
	engine.SaveConfig()
	engine.SaveCertificate(certPEM, privPEM)

	return engine, nil
}

func (e *Engine) handlePacket(conn *network.Connection, p protocol.Packet) {
	fmt.Printf("Received packet from %s: %s\n", conn.DeviceId, p.Type)

	switch p.Type {
	case "kdeconnect.pair":
		var pair protocol.PairBody
		if err := json.Unmarshal(p.Body, &pair); err != nil {
			fmt.Printf("Failed to unmarshal pair request: %v\n", err)
			return
		}
		if pair.Pair {
			remoteIP, _, _ := net.SplitHostPort(conn.Conn.RemoteAddr().String())

			// Calculate Verification Key
			var key string
			if tlsConn, ok := conn.Conn.(*tls.Conn); ok {
				peerCerts := tlsConn.ConnectionState().PeerCertificates
				if len(peerCerts) > 0 {
					myCert, _ := x509.ParseCertificate(e.Cert.Certificate[0])
					key, _ = protocol.GetVerificationKey(myCert, peerCerts[0], pair.Timestamp)
				}
			}

			// Ensure device is known before emitting event (important for AcceptPair)
			e.mu.RLock()
			_, exists := e.discoveredDevices[conn.DeviceId]
			isPending := e.pendingPairing[conn.DeviceId]
			e.mu.RUnlock()

			if isPending {
				e.mu.Lock()
				delete(e.pendingPairing, conn.DeviceId)
				e.mu.Unlock()
				e.MarkAsPaired(conn.DeviceId)
				return // Don't emit pair_request
			}

			if !exists {
				remoteIP, _, _ := net.SplitHostPort(conn.Conn.RemoteAddr().String())
				addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(remoteIP, fmt.Sprintf("%d", conn.RemoteIdentity.TcpPort)))
				e.addDiscoveredDevice(conn.RemoteIdentity, addr)
			}

			e.Events.Emit("pair_request", PairRequest{
				RemoteIP:        remoteIP,
				Identity:        conn.RemoteIdentity,
				VerificationKey: key,
			})
		} else {
			fmt.Printf("Received unpair request from %s\n", conn.DeviceId)
			e.Unpair(conn.DeviceId)
		}
	case "kdeconnect.ping":
		fmt.Println("Received Ping! Sending response...")
		conn.SendPacket("kdeconnect.ping", json.RawMessage("{}"))
	case "kdeconnect.sftp":
		var sftpBody protocol.SftpBody
		if err := json.Unmarshal(p.Body, &sftpBody); err == nil {
			if sftpBody.Port != 0 {
				fmt.Printf("Received SFTP offer from %s: %+v\n", conn.DeviceId, sftpBody)
				e.mu.Lock()
				e.sftpOffers[conn.DeviceId] = sftpBody
				e.mu.Unlock()
				e.Events.Emit("sftp_offer", conn.DeviceId)
			}
		}
	}
}

func (e *Engine) Start() {
	// Start Discovery
	err := network.StartDiscovery(e.Identity)
	if err != nil {
		log.Printf("Error starting discovery: %v", err)
	}

	// Listen Discovery
	go network.ListenDiscovery(func(p protocol.Packet, addr *net.UDPAddr) {
		if p.Type == "kdeconnect.identity" {
			var idBody protocol.IdentityBody
			if err := json.Unmarshal(p.Body, &idBody); err == nil {
				if idBody.DeviceId != e.Identity.DeviceId {
					e.addDiscoveredDevice(idBody, addr)
				}
			}
		}
	})

	// Start Server
	e.mu.RLock()
	server := &network.Server{
		Cert:     e.Cert,
		Port:     e.Identity.TcpPort,
		Identity: e.Identity,
		OnConnect: func(conn *network.Connection) {
			e.handleNewConnection(conn)
		},
	}
	e.btProvider.OnConnect = func(conn *network.Connection) {
		e.handleNewConnection(conn)
	}
	e.mu.RUnlock()

	go func() {
		if err := server.Start(); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	go func() {
		if err := e.btProvider.Start(); err != nil {
			log.Printf("Bluetooth error: %v", err)
		}
	}()
}

func (e *Engine) handleNewConnection(conn *network.Connection) {
	deviceId := conn.DeviceId
	e.mu.Lock()
	// If there is an existing connection, maybe close it or keep the newest one?
	// KDE Connect usually prefers the newer one for LAN, but Bluetooth might be a backup.
	e.activeConns[deviceId] = conn
	e.mu.Unlock()

	// Also treat as discovered if it's new to us or address updated
	remoteIP, _, _ := net.SplitHostPort(conn.Conn.RemoteAddr().String())
	addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(remoteIP, fmt.Sprintf("%d", conn.RemoteIdentity.TcpPort)))
	e.addDiscoveredDevice(conn.RemoteIdentity, addr)

	conn.OnPacket = func(p protocol.Packet) {
		e.handlePacket(conn, p)
	}
	conn.OnDisconnect = func() {
		e.mu.Lock()
		// Only delete if it's the SAME connection
		if e.activeConns[deviceId] == conn {
			delete(e.activeConns, deviceId)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) IsPaired(deviceId string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.pairedDevices[deviceId]
	return ok
}

func (e *Engine) IsDiscovered(deviceId string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.discoveredDevices[deviceId]
	return ok
}

func (e *Engine) GetSftpOffer(deviceId string) (protocol.SftpBody, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	offer, ok := e.sftpOffers[deviceId]
	return offer, ok
}

func (e *Engine) getOrConnect(deviceId string) (*network.Connection, error) {
	e.mu.RLock()
	conn, ok := e.activeConns[deviceId]
	e.mu.RUnlock()

	if ok {
		return conn, nil
	}

	e.mu.RLock()
	dev, discovered := e.discoveredDevices[deviceId]
	info, paired := e.pairedDevices[deviceId]
	e.mu.RUnlock()

	var ip string
	var port int
	if discovered {
		ip = dev.Addr.IP.String()
		port = dev.Identity.TcpPort
	} else if paired {
		ip = info.LastIP
		port = info.LastPort
	} else {
		return nil, fmt.Errorf("device %s not found", deviceId)
	}

	if ip == "" || port == 0 {
		return nil, fmt.Errorf("missing address for device %s", deviceId)
	}

	newConn, err := network.Connect(ip, port, e.Cert, e.Identity)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.activeConns[deviceId] = newConn
	e.mu.Unlock()

	newConn.OnPacket = func(p protocol.Packet) {
		e.handlePacket(newConn, p)
	}
	newConn.OnDisconnect = func() {
		e.mu.Lock()
		delete(e.activeConns, deviceId)
		e.mu.Unlock()
	}
	go newConn.StartLoop()

	return newConn, nil
}

func (e *Engine) SendPacket(deviceId string, pType string, body interface{}) error {
	conn, err := e.getOrConnect(deviceId)
	if err != nil {
		return err
	}
	return conn.SendPacket(pType, body)
}

func (e *Engine) triggerSftpBrowse(deviceId string) error {
	fmt.Printf("Sending SFTP browse request to %s...\n", deviceId)

	return e.SendPacket(deviceId, "kdeconnect.sftp.request", protocol.SftpBody{
		StartBrowsing: true,
	})
}

func (e *Engine) MarkAsPaired(deviceId string) {
	e.mu.Lock()
	if dev, ok := e.discoveredDevices[deviceId]; ok {
		e.pairedDevices[deviceId] = PairedDeviceInfo{
			Identity: dev.Identity,
			LastIP:   dev.Addr.IP.String(),
			LastPort: dev.Addr.Port,
		}
	}
	e.mu.Unlock()
	e.SaveConfig()
	e.Events.Emit("pairing_changed", deviceId)
}

func (e *Engine) GetPairedDevices() []PairedDeviceInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	devices := make([]PairedDeviceInfo, 0, len(e.pairedDevices))
	for _, dev := range e.pairedDevices {
		devices = append(devices, dev)
	}
	return devices
}

func (e *Engine) addDiscoveredDevice(identity protocol.IdentityBody, addr *net.UDPAddr) {
	e.mu.Lock()
	dev := DiscoveredDevice{Identity: identity, Addr: addr}
	e.discoveredDevices[identity.DeviceId] = dev

	// Update paired device info if it exists to persist last known IP
	changed := false
	if info, ok := e.pairedDevices[identity.DeviceId]; ok {
		if info.LastIP != addr.IP.String() || info.Identity.DeviceName != identity.DeviceName {
			info.LastIP = addr.IP.String()
			info.LastPort = addr.Port
			info.Identity = identity
			e.pairedDevices[identity.DeviceId] = info
			changed = true
		}
	}
	e.mu.Unlock()

	if changed {
		e.SaveConfig()
	}

	e.Events.Emit("device_discovered", dev)
}

func (e *Engine) Pair(deviceId string) error {
	e.mu.Lock()
	e.pendingPairing[deviceId] = true
	e.mu.Unlock()

	return e.SendPacket(deviceId, "kdeconnect.pair", protocol.PairBody{
		Pair:      true,
		Timestamp: time.Now().Unix(),
	})
}

func (e *Engine) Unpair(deviceId string) error {
	e.mu.Lock()
	_, ok := e.pairedDevices[deviceId]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("device not paired")
	}
	delete(e.pairedDevices, deviceId)
	e.mu.Unlock()

	e.SaveConfig()
	e.Events.Emit("pairing_changed", deviceId)

	// Try to send unpair request if we can connect
	err := e.SendPacket(deviceId, "kdeconnect.pair", protocol.PairBody{
		Pair:      false,
		Timestamp: time.Now().Unix(),
	})
	if err != nil {
		fmt.Printf("Could not send unpair request: %v\n", err)
	}

	return nil
}

func (e *Engine) AcceptPair(remoteIP string) {
	e.mu.RLock()
	var targetConn *network.Connection
	for _, conn := range e.activeConns {
		if ip, _, _ := net.SplitHostPort(conn.Conn.RemoteAddr().String()); ip == remoteIP {
			targetConn = conn
			break
		}
	}
	e.mu.RUnlock()

	if targetConn != nil {
		err := targetConn.SendPacket("kdeconnect.pair", protocol.PairBody{
			Pair:      true,
			Timestamp: time.Now().Unix(),
		})
		if err != nil {
			fmt.Printf("Error sending pair response: %v\n", err)
		}
	} else {
		// If no active connection, we might need to initiate one?
		// But usually we receive a pair request over a connection.
		fmt.Printf("AcceptPair: No active connection found for %s\n", remoteIP)
	}
}

func (e *Engine) GetDeviceByIP(ip string) (DiscoveredDevice, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, dev := range e.discoveredDevices {
		if dev.Addr.IP.String() == ip {
			return dev, true
		}
	}
	return DiscoveredDevice{}, false
}

func (e *Engine) ConnectSFTP(deviceId string) (*sftp.Client, error) {
	e.mu.RLock()
	dev, ok := e.discoveredDevices[deviceId]
	e.mu.RUnlock()

	iPaired := e.IsPaired(deviceId)

	if !ok {
		if !iPaired {
			return nil, fmt.Errorf("device not found and not paired")
		}

		// If paired, try to use the last known IP/Port
		e.mu.RLock()
		pd, hasPd := e.pairedDevices[deviceId]
		e.mu.RUnlock()
		if hasPd && pd.LastIP != "" {
			fmt.Printf("Device %s not discovered, attempting last known address: %s:%d\n", deviceId, pd.LastIP, pd.LastPort)
			addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(pd.LastIP, fmt.Sprintf("%d", pd.LastPort)))
			dev = DiscoveredDevice{
				Identity: pd.Identity,
				Addr:     addr,
			}
		} else {
			fmt.Printf("Device %s is paired but not yet discovered. Waiting for discovery...\n", deviceId)
			// Wait for discovery event
			foundChan := make(chan DiscoveredDevice, 1)
			var dHandler events.Listener
			dHandler = func(data interface{}) {
				d := data.(DiscoveredDevice)
				if d.Identity.DeviceId == deviceId {
					select {
					case foundChan <- d:
					default:
					}
				}
			}
			e.Events.On("device_discovered", dHandler)

			select {
			case dev = <-foundChan:
				fmt.Printf("Device %s discovered just in time!\n", deviceId)
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("device not found (timed out waiting for discovery)")
			}
		}
	}

	// 1. Prepare to wait for offer
	offerChan := make(chan protocol.SftpBody, 1)
	var handler events.Listener
	handler = func(data interface{}) {
		id := data.(string)
		if id == deviceId {
			e.mu.RLock()
			offer, ok := e.sftpOffers[deviceId]
			e.mu.RUnlock()
			if ok {
				select {
				case offerChan <- offer:
				default:
				}
			}
		}
	}
	e.Events.On("sftp_offer", handler)
	defer e.Events.Off("sftp_offer", handler)

	// Check if we already have a recent offer (less than 30 seconds old)
	e.mu.RLock()
	existingOffer, hasExisting := e.sftpOffers[deviceId]
	e.mu.RUnlock()
	if hasExisting && existingOffer.Port != 0 {
		fmt.Println("Using existing SFTP offer.")
		select {
		case offerChan <- existingOffer:
		default:
		}
	}

	// 2. Send startBrowsing request
	if err := e.triggerSftpBrowse(deviceId); err != nil {
		return nil, err
	}

	fmt.Println("Waiting for SFTP offer...")
	var offer protocol.SftpBody
	select {
	case offer = <-offerChan:
		fmt.Printf("Got SFTP offer: %+v\n", offer)
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for SFTP offer")
	}

	if offer.ErrorMessage != "" {
		return nil, fmt.Errorf("remote error: %s", offer.ErrorMessage)
	}

	if offer.Port == 0 {
		return nil, fmt.Errorf("no port provided in SFTP offer")
	}

	config := &ssh.ClientConfig{
		User: offer.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(offer.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(dev.Addr.IP.String(), fmt.Sprintf("%d", offer.Port))
	fmt.Printf("Dialing SFTP at %s\n", addr)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial failed: %w", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("sftp client failed: %w", err)
	}

	return sftpClient, nil
}

func getBluetoothAddress() string {
	// macOS implementation
	out, err := exec.Command("system_profiler", "SPBluetoothDataType").Output()
	if err != nil {
		return ""
	}
	// Parse something like "Address: CC:08:FA:6F:69:FA"
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "address:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				addr := strings.TrimSpace(strings.Join(parts[1:], ":"))
				if addr != "" {
					return addr
				}
			}
		}
	}
	return ""
}
