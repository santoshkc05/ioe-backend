// Command keygen prints a new Ed25519 key pair for JWT_SIGNING_KEY and JWT_VERIFY_KEYS.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
}

func run() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pk, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8}))
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pk}))
	oneLine := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", `\n`) }

	fmt.Println("# Keep the private key secret. Single-line values for .env:")
	fmt.Printf("JWT_SIGNING_KEY=%s\n", oneLine(privPEM))
	fmt.Println("# Public key, for JWT_VERIFY_KEYS (kid=PEM) after rotating to a new signing key:")
	fmt.Println(oneLine(pubPEM))
	return nil
}
