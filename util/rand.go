package util

import (
	"crypto/rand"
	"encoding/hex"
)

func RandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
