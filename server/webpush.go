package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Web Push (RFC 8030) med VAPID (RFC 8292) och aes128gcm-kryptering (RFC 8291),
// så att webbappen kan få larm som notiser på mobilen utan någon extern tjänst.

var b64u = base64.RawURLEncoding

// newVAPID skapar ett nyckelpar. Den privata nyckeln sparas som PKCS#8 (base64).
func newVAPID() (pub, priv string, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", "", err
	}
	ek, err := k.ECDH()
	if err != nil {
		return "", "", err
	}
	return b64u.EncodeToString(ek.PublicKey().Bytes()), base64.StdEncoding.EncodeToString(der), nil
}

func parseVAPID(priv string) (*ecdsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC key")
	}
	return ek, nil
}

// WebPushSub är en webbläsares prenumeration.
type WebPushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func b64any(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// encryptWebPush krypterar payload för prenumerationen (aes128gcm, en post).
func encryptWebPush(sub WebPushSub, payload []byte) ([]byte, error) {
	uaRaw, err := b64any(sub.Keys.P256dh)
	if err != nil {
		return nil, err
	}
	auth, err := b64any(sub.Keys.Auth)
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaRaw)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, append(append([]byte{}, payload...), 0x02), nil)
	var b bytes.Buffer
	b.Write(salt)
	_ = binary.Write(&b, binary.BigEndian, uint32(4096))
	b.WriteByte(byte(len(asPub)))
	b.Write(asPub)
	b.Write(ct)
	return b.Bytes(), nil
}

// vapidAuth bygger Authorization-huvudet för endpointen.
func vapidAuth(endpoint string, k *ecdsa.PrivateKey, pub, subject string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	head := b64u.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": subject})
	signing := head + "." + b64u.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64u.EncodeToString(sig) + ", k=" + pub, nil
}

var errGone = errors.New("subscription gone")

// sendWebPush skickar en notis. errGone betyder att prenumerationen ska tas bort.
func sendWebPush(sub WebPushSub, payload []byte, k *ecdsa.PrivateKey, pub, subject string, urgent bool) error {
	body, err := encryptWebPush(sub, payload)
	if err != nil {
		return err
	}
	auth, err := vapidAuth(sub.Endpoint, k, pub, subject)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Authorization", auth)
	if urgent {
		req.Header.Set("Urgency", "high")
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 || res.StatusCode == 410 {
		return errGone
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("push service responded %d: %s", res.StatusCode, b)
	}
	return nil
}
