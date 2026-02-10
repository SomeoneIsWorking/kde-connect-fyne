package core

import (
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

type PairedDeviceInfo struct {
	Identity protocol.IdentityBody `json:"identity"`
	LastIP   string                `json:"lastIP"`
	LastPort int                   `json:"lastPort"`
}

type Config struct {
	Identity      protocol.IdentityBody       `json:"identity"`
	PairedDevices map[string]PairedDeviceInfo `json:"pairedDevices"`
}

func GetConfigDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "kde-connect-fyne")
	os.MkdirAll(dir, 0700)
	return dir
}

func (e *Engine) SaveConfig() error {
	dir := GetConfigDir()

	e.mu.RLock()
	config := Config{
		Identity:      e.Identity,
		PairedDevices: e.pairedDevices,
	}
	e.mu.RUnlock()

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0600)
}

func (e *Engine) LoadConfig() error {
	dir := GetConfigDir()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}

	// Use a temporary structure to catch the raw JSON of paired devices
	var raw struct {
		Identity      protocol.IdentityBody `json:"identity"`
		PairedDevices json.RawMessage       `json:"pairedDevices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	e.mu.Lock()
	e.Identity = raw.Identity
	if e.pairedDevices == nil {
		e.pairedDevices = make(map[string]PairedDeviceInfo)
	}
	e.mu.Unlock()

	if len(raw.PairedDevices) == 0 {
		return nil
	}

	// Try unmarshaling as new format
	var newFormat map[string]PairedDeviceInfo
	if err := json.Unmarshal(raw.PairedDevices, &newFormat); err == nil {
		// Verify it's actually the new format (identity field must not be empty if map not empty)
		isNew := true
		for _, v := range newFormat {
			if v.Identity.DeviceId == "" {
				isNew = false
				break
			}
		}
		if isNew && len(newFormat) > 0 {
			e.mu.Lock()
			e.pairedDevices = newFormat
			e.mu.Unlock()
			return nil
		}
	}

	// Fallback to old format
	var oldFormat map[string]protocol.IdentityBody
	if err := json.Unmarshal(raw.PairedDevices, &oldFormat); err == nil {
		e.mu.Lock()
		for k, v := range oldFormat {
			e.pairedDevices[k] = PairedDeviceInfo{Identity: v}
		}
		e.mu.Unlock()
	}

	return nil
}

func (e *Engine) SaveCertificate(certPEM, privPEM []byte) error {
	dir := GetConfigDir()
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "key.pem"), privPEM, 0600)
}

func (e *Engine) LoadCertificate() (*tls.Certificate, error) {
	dir := GetConfigDir()
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(dir, "cert.pem"),
		filepath.Join(dir, "key.pem"),
	)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
