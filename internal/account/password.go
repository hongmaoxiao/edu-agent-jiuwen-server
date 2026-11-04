package account

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PBKDF2-SHA256 from the standard library; 600k iterations follows the OWASP 2023 guidance.
const passwordIterations = 600_000

var b64 = base64.RawStdEncoding

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func checkPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	salt, errSalt := b64.DecodeString(parts[2])
	want, errKey := b64.DecodeString(parts[3])
	if err != nil || errSalt != nil || errKey != nil || iterations < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash keeps a login for an unknown account as slow as one with a wrong password.
var dummyHash, _ = HashPassword(rand.Text())

func ValidPassword(password string) error {
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		return fmt.Errorf("%w: 密码长度要在 8 到 128 个字符之间", ErrRejected)
	}
	return nil
}

// InitialPassword is shown once to whoever opens the account; the holder must change it at first sign-in.
func InitialPassword() string { return rand.Text()[:12] }
