//go:build !darwin

package network

import "fmt"

func (b *BluetoothLinkProvider) startDarwin() error {
	return fmt.Errorf("bluetooth bridge not supported on this platform")
}
