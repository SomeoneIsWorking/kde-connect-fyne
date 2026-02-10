//go:build darwin

package network

/*
#cgo LDFLAGS: -L${SRCDIR} -lbluetooth_bridge -framework Foundation -framework IOBluetooth
#include <stdlib.h>
#include <stdint.h>
#include "bluetooth_bridge.h"

void goConnectionCallback(int channelID);
void goDataCallback(int channelID, uint8_t* data, int length);

static void inline_set_callbacks() {
    setConnectionCallback(goConnectionCallback);
	setDataCallback(goDataCallback);
}
*/
import "C"

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
	"unsafe"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

var (
	btConns   = make(map[int]*btConn)
	btConnsMu sync.Mutex
)

type btConn struct {
	id     int
	readRd *io.PipeReader
	readWr *io.PipeWriter
}

func (c *btConn) Read(b []byte) (n int, err error) {
	return c.readRd.Read(b)
}

func (c *btConn) Write(b []byte) (n int, err error) {
	res := C.writeToChannel(C.int(c.id), (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)))
	if res != 0 {
		return 0, io.EOF
	}
	return len(b), nil
}

func (c *btConn) Close() error {
	C.closeChannel(C.int(c.id))
	return c.readWr.Close()
}

func (c *btConn) LocalAddr() net.Addr                { return &net.TCPAddr{IP: net.IPv4zero, Port: 0} }
func (c *btConn) RemoteAddr() net.Addr               { return &net.TCPAddr{IP: net.IPv4zero, Port: 0} }
func (c *btConn) SetDeadline(t time.Time) error      { return nil }
func (c *btConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *btConn) SetWriteDeadline(t time.Time) error { return nil }

//export goConnectionCallback
func goConnectionCallback(channelID C.int) {
	id := int(channelID)
	rd, wr := io.Pipe()
	conn := &btConn{
		id:     id,
		readRd: rd,
		readWr: wr,
	}

	btConnsMu.Lock()
	btConns[id] = conn
	btConnsMu.Unlock()

	fmt.Printf("Go: New RFCOMM connection, ID: %d\n", id)

	if globalBluetoothProvider != nil && globalBluetoothProvider.OnConnect != nil {
		go func() {
			defer conn.Close()
			reader := bufio.NewReader(conn)

			// 1. Read their Identity (Plain)
			line, err := reader.ReadBytes('\n')
			if err != nil {
				fmt.Printf("Go: Bluetooth failed to read identity: %v\n", err)
				return
			}

			var p protocol.Packet
			var remoteIdentity protocol.IdentityBody
			if err := json.Unmarshal(line, &p); err != nil {
				fmt.Printf("Go: Bluetooth invalid identity packet: %v\n", err)
				return
			}
			if err := json.Unmarshal(p.Body, &remoteIdentity); err != nil {
				fmt.Printf("Go: Bluetooth invalid identity body: %v\n", err)
				return
			}

			tlsConfig := &tls.Config{
				Certificates:       []tls.Certificate{*globalBluetoothProvider.Cert},
				ClientAuth:         tls.RequestClientCert,
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
				VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
					return nil // Trust any client certificate
				},
			}

			// Reverse TLS: If we are the bridge on macOS, we act as the server for the incoming RFCOMM channel
			// But KDE Connect sometimes reverses these roles. Let's try to match the Server logic.
			bufferedConn := &BufferedConn{conn, reader}
			tlsConn := tls.Client(bufferedConn, tlsConfig)

			err = tlsConn.Handshake()
			if err != nil {
				fmt.Printf("Go: Bluetooth TLS Handshake failed: %v\n", err)
				return
			}

			// 2. Send our identity packet inside TLS
			packetBody, _ := json.Marshal(globalBluetoothProvider.Identity)
			idPacket := protocol.Packet{
				Id:   time.Now().UnixMilli(),
				Type: "kdeconnect.identity",
				Body: packetBody,
			}
			idData, _ := json.Marshal(idPacket)
			idData = append(idData, '\n')
			tlsConn.Write(idData)

			nc := NewConnection(tlsConn, remoteIdentity.DeviceId, remoteIdentity)
			globalBluetoothProvider.OnConnect(nc)
		}()
	}
}

//export goDataCallback
func goDataCallback(channelID C.int, data *C.uint8_t, length C.int) {
	id := int(channelID)
	btConnsMu.Lock()
	conn, ok := btConns[id]
	btConnsMu.Unlock()

	if ok {
		buf := C.GoBytes(unsafe.Pointer(data), length)
		conn.readWr.Write(buf)
	}
}

var globalBluetoothProvider *BluetoothLinkProvider

func (b *BluetoothLinkProvider) startDarwin() error {
	globalBluetoothProvider = b
	C.initBluetooth()
	C.inline_set_callbacks()

	serviceName := C.CString("KDE Connect")
	serviceUUID := C.CString("185f3df4-3268-4e3f-9fca-d4d5059915bd")
	defer C.free(unsafe.Pointer(serviceName))
	defer C.free(unsafe.Pointer(serviceUUID))

	res := C.startRFCOMMListener(serviceName, serviceUUID)
	if res != 0 {
		return fmt.Errorf("failed to start RFCOMM listener in Swift")
	}

	return nil
}
