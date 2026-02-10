package network

import (
	"crypto/tls"
	"log"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

type BluetoothLinkProvider struct {
	Identity  protocol.IdentityBody
	Cert      *tls.Certificate
	OnConnect func(conn *Connection)
}

func NewBluetoothLinkProvider(id protocol.IdentityBody, cert *tls.Certificate) *BluetoothLinkProvider {
	return &BluetoothLinkProvider{
		Identity: id,
		Cert:     cert,
	}
}

func (b *BluetoothLinkProvider) Start() error {
	// KDE Connect uses Classic Bluetooth RFCOMM with SERVICE_UUID: 185f3df4-3268-4e3f-9fca-d4d5059915bd

	err := b.startDarwin()
	if err == nil {
		return nil
	}

	log.Printf("BluetoothLinkProvider: Classic Bluetooth (RFCOMM) is not yet implemented for generic platforms. Error: %v", err)
	log.Printf("Advertised Bluetooth Address: %s", b.Identity.BluetoothAddress)

	return nil
}

func (b *BluetoothLinkProvider) Stop() {
	// Stop scanning/listening
}
