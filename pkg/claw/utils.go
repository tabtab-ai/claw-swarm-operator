package claw

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	nameCharset      = "abcdefghijklmnopqrstuvwxyz0123456789"
	nameCharsetFirst = "abcdefghijklmnopqrstuvwxyz"
)

func RandomName(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("length must be positive")
	}
	result := make([]byte, length)
	n, err := rand.Int(rand.Reader, big.NewInt(int64(26)))
	if err != nil {
		return "", fmt.Errorf("genearete fist character error: %w", err)
	}
	result[0] = nameCharsetFirst[n.Int64()]

	charsetLen := big.NewInt(int64(len(nameCharset)))
	for i := 1; i < len(result); i++ {
		n, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return "", fmt.Errorf("random name generation failed: %w", err)
		}
		result[i] = nameCharset[n.Int64()]
	}
	return string(result), nil
}
