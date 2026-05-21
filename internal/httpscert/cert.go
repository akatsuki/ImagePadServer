package httpscert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"imagepadserver/internal/settings"
)

type Files struct {
	CertPath string
	KeyPath  string
	Trusted  bool
}

func Ensure(hosts []string) (Files, error) {
	certDir := filepath.Join(settings.Dir(), "certs")
	certPath := filepath.Join(certDir, "imagepadserver.crt")
	keyPath := filepath.Join(certDir, "imagepadserver.key")

	if certificateMatches(certPath, hosts) {
		trusted := trustCertificate(certPath)
		return Files{CertPath: certPath, KeyPath: keyPath, Trusted: trusted}, nil
	}
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return Files{}, err
	}
	if err := writeCertificate(certPath, keyPath, hosts); err != nil {
		return Files{}, err
	}
	trusted := trustCertificate(certPath)
	return Files{CertPath: certPath, KeyPath: keyPath, Trusted: trusted}, nil
}

func certificateMatches(path string, hosts []string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || time.Now().After(cert.NotAfter.Add(-30*24*time.Hour)) {
		return false
	}
	for _, host := range hosts {
		if err := cert.VerifyHostname(host); err != nil {
			return false
		}
	}
	return true
}

func writeCertificate(certPath, keyPath string, hosts []string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "ImagePadServer Local HTTPS",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if host != "" {
			template.DNSNames = append(template.DNSNames, host)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyFile.Close()
	return pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
