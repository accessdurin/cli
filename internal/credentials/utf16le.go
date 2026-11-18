package credentials

// UTF-16LE codec for the Windows credential blob (see store_windows.go). It
// is platform-independent so its edge cases are tested on every OS.

import (
	"errors"
	"unicode/utf16"
)

func encodeUTF16LE(value string) []byte {
	units := utf16.Encode([]rune(value))
	blob := make([]byte, 2*len(units))
	for i, u := range units {
		blob[2*i] = byte(u)
		blob[2*i+1] = byte(u >> 8)
	}
	clear(units)
	return blob
}

// decodeUTF16LE strictly decodes a UTF-16LE blob, like Rust's
// String::from_utf16: odd lengths and unpaired surrogates are errors.
func decodeUTF16LE(blob []byte) (string, error) {
	if len(blob)%2 != 0 {
		return "", errors.New("credential manager: stored secret has odd length")
	}
	units := make([]uint16, len(blob)/2)
	defer clear(units)
	for i := range units {
		units[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
	}
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case u >= 0xD800 && u < 0xDC00:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] >= 0xE000 {
				return "", errors.New("credential manager: stored secret is not UTF-16")
			}
			i++
		case u >= 0xDC00 && u < 0xE000:
			return "", errors.New("credential manager: stored secret is not UTF-16")
		}
	}
	return string(utf16.Decode(units)), nil
}
