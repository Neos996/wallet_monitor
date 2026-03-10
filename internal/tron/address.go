package tron

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const tronAddressPrefix = byte(0x41)

var base58Alphabet = []byte("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")

// AddressToHex converts a Tron base58 or hex address into a Tron hex address (41-prefixed).
func AddressToHex(address string) (string, error) {
	addr := strings.TrimSpace(address)
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}
	if strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X") {
		addr = addr[2:]
	}

	if isHexString(addr) {
		switch len(addr) {
		case 42:
			return strings.ToLower(addr), nil
		case 40:
			return "41" + strings.ToLower(addr), nil
		}
	}

	payload, err := base58CheckDecode(addr)
	if err != nil {
		return "", err
	}
	if len(payload) != 21 || payload[0] != tronAddressPrefix {
		return "", fmt.Errorf("invalid tron address")
	}
	return hex.EncodeToString(payload), nil
}

// HexToAddress converts a Tron hex address (41-prefixed) into a base58 address.
func HexToAddress(address string) (string, error) {
	addr := strings.TrimSpace(address)
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}
	if strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X") {
		addr = addr[2:]
	}

	if len(addr) == 40 {
		addr = "41" + addr
	}

	raw, err := hex.DecodeString(addr)
	if err != nil {
		return "", fmt.Errorf("decode hex failed")
	}
	if len(raw) != 21 || raw[0] != tronAddressPrefix {
		return "", fmt.Errorf("invalid tron address")
	}

	return base58CheckEncode(raw), nil
}

func base58CheckDecode(input string) ([]byte, error) {
	decoded, err := base58Decode(input)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 5 {
		return nil, fmt.Errorf("invalid address length")
	}

	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]
	hash := sha256.Sum256(payload)
	hash = sha256.Sum256(hash[:])

	if !bytes.Equal(checksum, hash[:4]) {
		return nil, fmt.Errorf("checksum mismatch")
	}

	return payload, nil
}

func base58Decode(input string) ([]byte, error) {
	result := big.NewInt(0)
	base := big.NewInt(58)

	for i := 0; i < len(input); i++ {
		char := input[i]
		index := bytes.IndexByte(base58Alphabet, char)
		if index < 0 {
			return nil, fmt.Errorf("invalid base58 character")
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(index)))
	}

	decoded := result.Bytes()

	leadingZeros := 0
	for i := 0; i < len(input) && input[i] == base58Alphabet[0]; i++ {
		leadingZeros++
	}

	output := make([]byte, leadingZeros+len(decoded))
	copy(output[leadingZeros:], decoded)
	return output, nil
}

func isHexString(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func base58CheckEncode(payload []byte) string {
	hash := sha256.Sum256(payload)
	hash = sha256.Sum256(hash[:])
	checksum := hash[:4]

	fullPayload := append(payload, checksum...)
	return base58Encode(fullPayload)
}

func base58Encode(input []byte) string {
	if len(input) == 0 {
		return ""
	}

	num := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var result []byte
	for num.Cmp(zero) > 0 {
		num.DivMod(num, base, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}

	for _, b := range input {
		if b == 0 {
			result = append(result, base58Alphabet[0])
		} else {
			break
		}
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}
