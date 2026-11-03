// Package dpop holds a client's own proof-of-possession key and signs RFC 9449
// DPoP proofs with it (TENG-3693 G05).
//
// The handoff runner proves this key when it claims a run, and Telara binds the
// run's grant session to it. Every later call on that session must carry a proof
// by the same key, so a copy of the session credential used by another program is
// refused. The private key never leaves this package: it is not printed, not
// marshalled and not written anywhere but its own file.
package dpop

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Header is the request header a proof travels in.
const Header = "DPoP"

// Key is a P-256 key used only to sign proofs. Its zero value is unusable.
type Key struct {
	private    *ecdsa.PrivateKey
	jwk        map[string]string
	thumbprint string
}

// ErrNoKey means the key file does not exist yet.
var ErrNoKey = errors.New("no DPoP key")

// Generate makes a new key held in memory only.
func Generate() (*Key, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate DPoP key: %w", err)
	}
	return fromPrivate(priv)
}

func fromPrivate(priv *ecdsa.PrivateKey) (*Key, error) {
	if priv == nil || priv.Curve != elliptic.P256() {
		return nil, errors.New("DPoP key must be P-256")
	}
	pub, err := priv.PublicKey.ECDH()
	if err != nil {
		return nil, errors.New("DPoP key is not a valid P-256 key")
	}
	raw := pub.Bytes() // 0x04 || X || Y, 32 bytes each
	x := base64.RawURLEncoding.EncodeToString(raw[1:33])
	y := base64.RawURLEncoding.EncodeToString(raw[33:65])
	// RFC 7638: the required members only, in lexicographic order, no whitespace.
	sum := sha256.Sum256([]byte(fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":%q,"y":%q}`, x, y)))
	return &Key{
		private:    priv,
		jwk:        map[string]string{"kty": "EC", "crv": "P-256", "x": x, "y": y},
		thumbprint: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// Thumbprint is the RFC 7638 SHA-256 thumbprint of the public key, the value a
// bound session records.
func (k *Key) Thumbprint() string {
	if k == nil {
		return ""
	}
	return k.thumbprint
}

// String names the key by its thumbprint, so printing a Key never prints the key.
func (k *Key) String() string { return "dpop.Key(" + k.Thumbprint() + ")" }

// GoString is String, for %#v.
func (k *Key) GoString() string { return k.String() }

// MarshalJSON refuses, so a Key can never be encoded into a journal or an event.
func (k *Key) MarshalJSON() ([]byte, error) {
	return nil, errors.New("a DPoP key is never serialised")
}

// Proof signs a proof for one request. accessToken is the credential presented in
// the same request's Authorization header; the proof carries its hash (ath), so a
// proof made for one credential cannot be presented with another. The URL's query
// and fragment are dropped, as RFC 9449 section 4.2 requires.
func (k *Key) Proof(method, rawURL, accessToken string) (string, error) {
	if k == nil || k.private == nil {
		return "", errors.New("no DPoP key")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("DPoP proof needs an absolute request URL")
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("DPoP proof id: %w", err)
	}
	header, err := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": k.jwk})
	if err != nil {
		return "", err
	}
	claims := map[string]any{
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"htm": method,
		"htu": u.String(),
		"iat": time.Now().Unix(),
	}
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", errors.New("DPoP proof could not be signed")
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Create writes a new key to path, readable by its owner only. It never replaces
// an existing file.
func Create(path string) (*Key, error) {
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k.private)
	if err != nil {
		return nil, errors.New("DPoP key could not be encoded")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	clear(der)
	_, werr := f.Write(block)
	clear(block)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return nil, errors.New("DPoP key could not be written")
	}
	return k, nil
}

// Load reads the key at path. It refuses a key file anyone but its owner can read
// or write, and a symbolic link, since either would let another program prove it.
func Load(path string) (*Key, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the DPoP key %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the DPoP key %s must be readable by its owner only (mode 0600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("the DPoP key %s is not a PKCS#8 private key", path)
	}
	defer clear(block.Bytes)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the DPoP key %s could not be read", path)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the DPoP key %s is not an EC key", path)
	}
	return fromPrivate(priv)
}

// LoadOrCreate loads the key at path, creating it when there is none.
func LoadOrCreate(path string) (*Key, error) {
	k, err := Load(path)
	if errors.Is(err, ErrNoKey) {
		return Create(path)
	}
	return k, err
}
