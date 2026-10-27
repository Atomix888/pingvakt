package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"testing"
)

// decryptWebPush gör det webbläsaren gör, för att kontrollera krypteringen.
func decryptWebPush(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	if rs != 4096 || idlen != 65 {
		t.Fatalf("huvud: rs=%d idlen=%d", rs, idlen)
	}
	pk, _ := ecdh.P256().NewPublicKey(asPub)
	shared, _ := uaPriv.ECDH(pk)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pt[len(pt)-1] != 0x02 {
		t.Fatal("avgränsare saknas")
	}
	return pt[:len(pt)-1]
}

func TestWebPushRoundTrip(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var sub WebPushSub
	sub.Endpoint = "https://push.example.com/abc"
	sub.Keys.P256dh = b64u.EncodeToString(ua.PublicKey().Bytes())
	sub.Keys.Auth = b64u.EncodeToString(auth)
	body, err := encryptWebPush(sub, []byte(`{"title":"Hej"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptWebPush(t, body, ua, auth); string(got) != `{"title":"Hej"}` {
		t.Fatalf("fick %q", got)
	}
	pub, priv, err := newVAPID()
	if err != nil {
		t.Fatal(err)
	}
	k, err := parseVAPID(priv)
	if err != nil {
		t.Fatal(err)
	}
	h, err := vapidAuth(sub.Endpoint, k, pub, "mailto:test@example.com")
	if err != nil || !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, "k="+pub) {
		t.Fatalf("vapid: %v %s", err, h)
	}
}
