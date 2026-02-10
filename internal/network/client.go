package network

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/barishamil/kde-connect-fyne/internal/protocol"
)

func Connect(ip string, port int, cert *tls.Certificate, myIdentity protocol.IdentityBody) (*Connection, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, err
	}

	// 1. Send our Identity (Plain)
	if err := sendIdentity(conn, myIdentity); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send plain identity: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{*cert},
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return nil // Trust any client certificate (Self-signed)
		},
	}

	// Revert to Server mode (Reverse TLS) because Android acts as Client on Outgoing connections
	tlsConn := tls.Server(conn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls handshake failed: %v", err)
	}

	// 2. Send Identity (Encrypted)
	if err := sendIdentity(tlsConn, myIdentity); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("failed to send encrypted identity: %v", err)
	}

	// 3. Read Their Identity (Encrypted)
	reader := bufio.NewReader(tlsConn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("failed to read secure identity: %v", err)
	}
	var p protocol.Packet
	if err := json.Unmarshal(line, &p); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("invalid secure identity packet: %v", err)
	}
	var remoteIdentity protocol.IdentityBody
	if err := json.Unmarshal(p.Body, &remoteIdentity); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("invalid secure identity body: %v", err)
	}

	return NewConnection(tlsConn, remoteIdentity.DeviceId, remoteIdentity), nil
}

func sendIdentity(conn net.Conn, identity protocol.IdentityBody) error {
	packetBody, _ := json.Marshal(identity)
	packet := protocol.Packet{
		Id:   time.Now().UnixMilli(),
		Type: "kdeconnect.identity",
		Body: packetBody,
	}
	data, _ := json.Marshal(packet)
	data = append(data, '\n')
	_, err := conn.Write(data)
	return err
}
