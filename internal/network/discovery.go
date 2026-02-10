package network

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
	"github.com/grandcat/zeroconf"
)

const UDP_PORT = 1716

func StartDiscovery(id protocol.IdentityBody) error {
	packetBody, _ := json.Marshal(id)
	packet := protocol.Packet{
		Id:   time.Now().UnixMilli(),
		Type: "kdeconnect.identity",
		Body: packetBody,
	}

	data, _ := json.Marshal(packet)
	data = append(data, '\n')

	// 1. Start mDNS Responder
	go func() {
		// Service name should be the deviceId
		server, err := zeroconf.Register(
			id.DeviceId,
			"_kdeconnect._udp",
			"local.",
			id.TcpPort,
			[]string{
				"id=" + id.DeviceId,
				"name=" + id.DeviceName,
				"type=" + id.DeviceType,
				"protocol=" + fmt.Sprintf("%d", id.ProtocolVersion),
			},
			nil,
		)
		if err != nil {
			log.Printf("mDNS Error: %v", err)
			return
		}
		defer server.Shutdown()

		// Keep alive
		select {}
	}()

	// 2. Start UDP Broadcast
	broadcasts, err := getBroadcastAddresses()
	if err != nil {
		// Fallback to global broadcast if getting specific ones fails
		broadcasts = []string{"255.255.255.255"}
	}

	go func() {
		for {
			for _, ip := range broadcasts {
				addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ip, fmt.Sprintf("%d", UDP_PORT)))
				if err != nil {
					continue
				}

				conn, err := net.DialUDP("udp4", nil, addr)
				if err != nil {
					continue
				}
				_, _ = conn.Write(data)
				conn.Close()
			}
			time.Sleep(5 * time.Second)
		}
	}()

	return nil
}

func getBroadcastAddresses() ([]string, error) {
	var broadcasts []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			mask := ipnet.Mask
			broadcast := make(net.IP, len(ip))
			for i := 0; i < len(ip); i++ {
				broadcast[i] = ip[i] | ^mask[i]
			}
			broadcasts = append(broadcasts, broadcast.String())
		}
	}
	// Also include the global broadcast
	broadcasts = append(broadcasts, "255.255.255.255")
	return broadcasts, nil
}

func ListenDiscovery(handler func(protocol.Packet, *net.UDPAddr)) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", UDP_PORT))
	if err != nil {
		return
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return
	}
	defer conn.Close()

	buf := make([]byte, 2048)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		var p protocol.Packet
		if err := json.Unmarshal(buf[:n], &p); err == nil {
			handler(p, remoteAddr)
		}
	}
}
