package platform

import (
	"crypto/rand"
	"encoding/hex"
)

func ID(prefix string) string {
	var bytes [16]byte
	rand.Read(bytes[:])
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
