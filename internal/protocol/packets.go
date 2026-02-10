package protocol

import "encoding/json"

type Packet struct {
	Id   int64           `json:"id"`
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

type IdentityBody struct {
	DeviceId             string   `json:"deviceId"`
	DeviceName           string   `json:"deviceName"`
	DeviceType           string   `json:"deviceType"`
	ProtocolVersion      int      `json:"protocolVersion"`
	TcpPort              int      `json:"tcpPort"`
	BluetoothAddress     string   `json:"bluetoothAddress,omitempty"`
	IncomingCapabilities []string `json:"incomingCapabilities"`
	OutgoingCapabilities []string `json:"outgoingCapabilities"`
}

type PairBody struct {
	Pair      bool  `json:"pair"`
	Timestamp int64 `json:"timestamp,omitempty"`
}

type SftpBody struct {
	StartBrowsing bool     `json:"startBrowsing,omitempty"`
	Ip            string   `json:"ip,omitempty"`
	Port          int      `json:"port,omitempty"`
	User          string   `json:"user,omitempty"`
	Password      string   `json:"password,omitempty"`
	Path          string   `json:"path,omitempty"`
	MultiPaths    []string `json:"multiPaths,omitempty"`
	PathNames     []string `json:"pathNames,omitempty"`
	ErrorMessage  string   `json:"errorMessage,omitempty"`
}
