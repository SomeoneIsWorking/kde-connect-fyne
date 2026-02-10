package network

import (
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

type Connection struct {
	Conn           net.Conn
	DeviceId       string
	RemoteIdentity protocol.IdentityBody
	OnPacket       func(p protocol.Packet)
	OnDisconnect   func()

	mu sync.Mutex
}

func NewConnection(conn net.Conn, deviceId string, remoteIdentity protocol.IdentityBody) *Connection {
	return &Connection{
		Conn:           conn,
		DeviceId:       deviceId,
		RemoteIdentity: remoteIdentity,
	}
}

func (c *Connection) StartLoop() {
	decoder := json.NewDecoder(c.Conn)
	for {
		var p protocol.Packet
		if err := decoder.Decode(&p); err != nil {
			if c.OnDisconnect != nil {
				c.OnDisconnect()
			}
			return
		}
		if c.OnPacket != nil {
			c.OnPacket(p)
		}
	}
}

func (c *Connection) SendPacket(pType string, body interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}

	packet := protocol.Packet{
		Id:   time.Now().UnixMilli(),
		Type: pType,
		Body: bodyJSON,
	}

	data, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	_, err = c.Conn.Write(data)
	return err
}

func (c *Connection) Close() error {
	return c.Conn.Close()
}
