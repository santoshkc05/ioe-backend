package jwt

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Keys holds the signing key and every public key accepted for verification, by kid.
type Keys struct {
	SigningKID string
	Signing    ed25519.PrivateKey
	Verify     map[string]ed25519.PublicKey
}

// ParseKeys loads a PKCS#8 Ed25519 signing key and optional extra public keys given as
// "kid=PEM" entries separated by ";". PEM text may use literal "\n" for newlines.
func ParseKeys(signingPEM, signingKID, verifySpec string) (Keys, error) {
	if signingKID == "" {
		return Keys{}, errors.New("JWT_SIGNING_KEY_ID is empty")
	}
	priv, err := parsePrivate(signingPEM)
	if err != nil {
		return Keys{}, fmt.Errorf("JWT_SIGNING_KEY: %w", err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return Keys{}, errors.New("JWT_SIGNING_KEY: unexpected public key type")
	}
	k := Keys{SigningKID: signingKID, Signing: priv, Verify: map[string]ed25519.PublicKey{signingKID: pub}}
	for _, entry := range strings.Split(verifySpec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, pemText, ok := strings.Cut(entry, "=")
		if !ok || kid == "" {
			return Keys{}, errors.New("JWT_VERIFY_KEYS: each entry must be kid=PEM")
		}
		if _, dup := k.Verify[kid]; dup {
			return Keys{}, fmt.Errorf("JWT_VERIFY_KEYS: duplicate kid %q", kid)
		}
		pub, err := parsePublic(pemText)
		if err != nil {
			return Keys{}, fmt.Errorf("JWT_VERIFY_KEYS: kid %q: %w", kid, err)
		}
		k.Verify[kid] = pub
	}
	return k, nil
}

func decodePEM(s string) (*pem.Block, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(s, `\n`, "\n")))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return block, nil
}

func parsePrivate(s string) (ed25519.PrivateKey, error) {
	block, err := decodePEM(s)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	return ed, nil
}

func parsePublic(s string) (ed25519.PublicKey, error) {
	block, err := decodePEM(s)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 public key")
	}
	return ed, nil
}
