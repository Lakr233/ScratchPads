package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// LoadOrCreateCert returns the TLS certificate the server listens with.
// Explicit cert/key files win; otherwise a self-signed certificate is kept in
// dataDir. Its key is reused across restarts so the published pin stays valid,
// and the certificate is reissued when the hostname list changes or it nears
// expiry.
func LoadOrCreateCert(cfg Config) (tls.Certificate, error) {
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		return tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	}
	keyPath := filepath.Join(cfg.DataDir, "tls.key")
	certPath := filepath.Join(cfg.DataDir, "tls.crt")

	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, _ := x509.ParseCertificate(cert.Certificate[0])
		if leaf != nil && !leaf.IsCA && certMatches(leaf, cfg.Hostnames) && time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return cert, nil
		}
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "ScratchPads", Organization: []string{"ScratchPads self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range cfg.Hostnames {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, pemBytes, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("tls.key is not PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return key, os.WriteFile(path, raw, 0o600)
}

func certMatches(leaf *x509.Certificate, hostnames []string) bool {
	var names []string
	names = append(names, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) != len(hostnames) {
		return false
	}
	for _, h := range hostnames {
		if ip := net.ParseIP(h); ip != nil {
			h = ip.String()
		}
		if !slices.Contains(names, h) {
			return false
		}
	}
	return true
}

// PublicKeyPin is the value for curl --pinnedpubkey "sha256//<pin>".
func PublicKeyPin(cert tls.Certificate) (string, error) {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

func CertFingerprint(cert tls.Certificate) string {
	sum := sha256.Sum256(cert.Certificate[0])
	return fmt.Sprintf("%X", sum[:])
}

// TLSConfig serves ACME certificates for cfg.ACMEDomains and the self-signed
// certificate for every other name (localhost, IPs, internal hostnames).
func TLSConfig(cfg Config, selfSigned tls.Certificate) (*tls.Config, TLSInfo, error) {
	conf := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{selfSigned}}
	pin, err := PublicKeyPin(selfSigned)
	if err != nil {
		return nil, TLSInfo{}, err
	}
	if cfg.TLSCert != "" {
		return conf, TLSInfo{Mode: "custom"}, nil
	}
	if len(cfg.ACMEDomains) == 0 {
		return conf, TLSInfo{Mode: "self-signed", Pin: pin}, nil
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.ACMEDomains...),
		Cache:      autocert.DirCache(filepath.Join(cfg.DataDir, "acme")),
		Email:      cfg.ACMEEmail,
	}
	acme := map[string]bool{}
	for _, d := range cfg.ACMEDomains {
		acme[d] = true
	}
	conf.NextProtos = []string{"h2", "http/1.1", "acme-tls/1"}
	conf.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if !acme[hello.ServerName] {
			return &selfSigned, nil
		}
		return m.GetCertificate(hello)
	}
	// Warm the certificate in the background so the first visitor does not wait.
	go func() {
		for _, d := range cfg.ACMEDomains {
			hello := &tls.ClientHelloInfo{
				ServerName:       d,
				CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
				SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
				SupportedCurves:  []tls.CurveID{tls.CurveP256},
			}
			if _, err := m.GetCertificate(hello); err != nil {
				fmt.Fprintf(os.Stderr, "acme %s: %v\n", d, err)
			}
		}
	}()
	return conf, TLSInfo{Mode: "acme"}, nil
}
