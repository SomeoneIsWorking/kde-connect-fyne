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

type Server struct {
	Cert      *tls.Certificate
	Port      int
	Identity  protocol.IdentityBody
	OnConnect func(conn *Connection)
}

func (s *Server) Start() error {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", s.Port))
	if err != nil {
		return err
	}
	defer l.Close()

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go s.handleConnection(conn)
	}
}

type BufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (bc *BufferedConn) Read(b []byte) (int, error) {
	n, err := bc.r.Read(b)
	return n, err
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// 1. Read their Identity (Plain)
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		fmt.Printf("Failed to read identity: %v\n", err)
		return
	}

	var p protocol.Packet
	var remoteVersion int
	var remoteIdentity protocol.IdentityBody
	if err := json.Unmarshal(line, &p); err != nil {
		fmt.Printf("Invalid identity packet: %v\n", err)
		return
	}
	if err := json.Unmarshal(p.Body, &remoteIdentity); err != nil {
		fmt.Printf("Invalid identity body: %v\n", err)
		return
	}
	remoteVersion = remoteIdentity.ProtocolVersion

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{*s.Cert},
		ClientAuth:         tls.RequestClientCert,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return nil // Trust any client certificate
		},
	}

	// Wrap the reader and conn so we don't lose buffered bytes
	bufferedConn := &BufferedConn{conn, reader}
	// Revert to Client mode (Reverse TLS) because Android acts as Server on Incoming connections
	tlsConn := tls.Client(bufferedConn, tlsConfig)

	err = tlsConn.Handshake()
	if err != nil {
		fmt.Printf("TLS Handshake failed: %v\n", err)
		return
	}

	// 2. Send our identity packet inside TLS
	packetBody, _ := json.Marshal(s.Identity)
	idPacket := protocol.Packet{
		Id:   time.Now().UnixMilli(),
		Type: "kdeconnect.identity",
		Body: packetBody,
	}
	idData, _ := json.Marshal(idPacket)
	idData = append(idData, '\n')
	if _, err := tlsConn.Write(idData); err != nil {
		fmt.Printf("Failed to send secure identity: %v\n", err)
		return
	}

	// 3. Read their identity packet inside TLS
	if remoteVersion >= 8 {
		decoder := json.NewDecoder(tlsConn)
		var secureIdentity protocol.Packet
		if err := decoder.Decode(&secureIdentity); err != nil {
			fmt.Printf("failed to read secure identity: %v\n", err)
			return
		}
		if err := json.Unmarshal(secureIdentity.Body, &remoteIdentity); err != nil {
			fmt.Printf("Failed to unmarshal secure identity: %v\n", err)
			return
		}
	}

	c := NewConnection(tlsConn, remoteIdentity.DeviceId, remoteIdentity)

	if s.OnConnect != nil {
		s.OnConnect(c)
	}

	// Start the loop and block here (it will use the tlsConn)
	c.StartLoop()
}
