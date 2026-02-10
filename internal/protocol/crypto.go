package protocol

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

func GetVerificationKey(certA, certB *x509.Certificate, timestamp int64) (string, error) {
	pubA := certA.RawSubjectPublicKeyInfo
	pubB := certB.RawSubjectPublicKeyInfo

	// Sort descending (Largest + Smallest)
	// Kotlin: if (compareUnsigned(a, b) < 0) { b + a } else { a + b }
	// IMPORTANT: We must NOT use append(pubA, pubB...) directly as it might
	// overwrite the underlying buffer of pubA if there is capacity!
	combined := make([]byte, 0, len(pubA)+len(pubB)+32)
	if bytes.Compare(pubA, pubB) < 0 {
		combined = append(combined, pubB...)
		combined = append(combined, pubA...)
	} else {
		combined = append(combined, pubA...)
		combined = append(combined, pubB...)
	}

	// Append timestamp (only for protocol version >= 8, which we assume now)
	// Kotlin: timestamp.toString().toByteArray()
	// It uses standard string representation of the long.
	tsStr := fmt.Sprintf("%d", timestamp)
	combined = append(combined, []byte(tsStr)...)

	hash := sha256.Sum256(combined)
	// Hex string, first 8 chars, uppercase
	hexStr := fmt.Sprintf("%x", hash)
	if len(hexStr) > 8 {
		hexStr = hexStr[:8]
	}
	return strings.ToUpper(hexStr), nil
}

func GenerateCertificate(deviceName string) (tls.Certificate, []byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(3650 * 24 * time.Hour) // 10 years

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"KDE Connect"},
			CommonName:   deviceName,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	cert, err := tls.X509KeyPair(certPEM, privPEM)
	return cert, certPEM, privPEM, err
}
