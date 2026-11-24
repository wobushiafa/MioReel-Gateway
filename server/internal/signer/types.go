package signer

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// RandSource abstracts randomness so production uses crypto/rand and tests can inject
// deterministic sequences to match Node golden vectors byte-for-byte.
//
// Consumption order in Profile.Sign must remain:
//  1. 3 x Uint32() for packet randoms[0..2]
//  2. 1 x Uint16() for PublicProfileGorgon
//  3. 1 x Uint32Big() for PublicProfileHelios
type RandSource interface {
	Uint32() (uint32, error)    // masked to 31 bits (& 0x7fffffff)
	Uint16() (uint16, error)    // 0..65535
	Uint32Big() (uint32, error) // 0..0xffffffff
}

// CryptoRand implements RandSource using crypto/rand.Reader.
type CryptoRand struct{}

func (CryptoRand) Uint32() (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]) & 0x7fffffff, nil
}

func (CryptoRand) Uint16() (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b[:]), nil
}

func (CryptoRand) Uint32Big() (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// SigningError represents a structured internal signing failure matching doc §14.5.
type SigningError struct {
	Code    string
	Message string
}

func (e *SigningError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

func errCode(code, msg string) error {
	return &SigningError{Code: code, Message: msg}
}

var ErrMissingConfiguration = errors.New("SOURCE_CONFIGURATION_MISSING")

// Configuration holds signing profile credentials.
type Configuration struct {
	ID       []byte
	Material []byte
}

// Device identifies the upstream device session participating in signatures.
type Device struct {
	IID      string
	DeviceID string
	CDID     string
}

// SignInput is the input to Profile.Sign.
type SignInput struct {
	URL     string
	Body    []byte
	Epoch   uint32
	Device  Device
	Counter *int32 // optional override; if nil, profile increments its internal sequence
}

// ProduceInput is the input to Profile.Produce.
type ProduceInput struct {
	URL     string
	Body    []byte
	Epoch   uint32
	Device  Device
	Randoms [3]uint32
	Counter *int32
}

// ProduceResult holds intermediate artifacts useful for golden verification.
type ProduceResult struct {
	Proto     []byte
	Body      []byte
	Query     string
	Signature string // x-medusa base64
}

// SignHeaders contains the 6 signature headers produced by Profile.Sign.
type SignHeaders struct {
	XGorgon  string
	XKhronos string
	XLadon   string
	XArgus   string
	XHelios  string
	XMedusa  string
}

// Map returns the 6 headers as a lower-case HTTP header map.
func (h SignHeaders) Map() map[string]string {
	return map[string]string{
		"x-gorgon":  h.XGorgon,
		"x-khronos": h.XKhronos,
		"x-ladon":   h.XLadon,
		"x-argus":   h.XArgus,
		"x-helios":  h.XHelios,
		"x-medusa":  h.XMedusa,
	}
}
