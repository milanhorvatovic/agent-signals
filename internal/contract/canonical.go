package contract

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// CanonicalDigest returns the SHA-256 of the canonical serialization of a
// strict-decoded JSON value. It is the duplicate/conflict discriminator from
// event-contract.md §Spool and cursors: same ID + same digest is an accepted
// duplicate, same ID + different digest is a hard conflict.
func CanonicalDigest(v any) ([sha256.Size]byte, error) {
	buf, err := appendCanonical(nil, v)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(buf), nil
}

// appendCanonical serializes v per the byte-exact specification in the
// package documentation. v must be a strict-decode value tree; any other
// type is a programming error surfaced loudly.
func appendCanonical(dst []byte, v any) ([]byte, error) {
	switch val := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case bool:
		if val {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case json.Number:
		return append(dst, val...), nil
	case string:
		return appendCanonicalString(dst, val), nil
	case []any:
		dst = append(dst, '[')
		for i, elem := range val {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendCanonical(dst, elem); err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, compareUTF16)
		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendCanonicalString(dst, k)
			dst = append(dst, ':')
			var err error
			if dst, err = appendCanonical(dst, val[k]); err != nil {
				return nil, err
			}
		}
		return append(dst, '}'), nil
	default:
		return nil, fmt.Errorf("canonical serialization: unsupported type %T", v)
	}
}

// compareUTF16 orders two strings by their UTF-16 code units, the member
// ordering RFC 8785 specifies. It parts company with Go's native byte order
// only above the BMP: a supplementary character encodes as a surrogate pair
// starting at U+D800, which sorts below U+E000–U+FFFF, while the same
// character's UTF-8 bytes sort above them.
func compareUTF16(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			// Encoding only the deciding runes settles both the BMP case and
			// the pair-versus-pair one, where two supplementary characters
			// share a lead surrogate and the trail unit is what separates them.
			return slices.Compare(utf16.Encode([]rune{ra}), utf16.Encode([]rune{rb}))
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
}

const hexDigits = "0123456789abcdef"

func appendCanonicalString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\':
				dst = append(dst, '\\', b)
			case b == '\b':
				dst = append(dst, '\\', 'b')
			case b == '\f':
				dst = append(dst, '\\', 'f')
			case b == '\n':
				dst = append(dst, '\\', 'n')
			case b == '\r':
				dst = append(dst, '\\', 'r')
			case b == '\t':
				dst = append(dst, '\\', 't')
			case b < 0x20:
				dst = appendUnicodeEscape(dst, rune(b))
			default:
				dst = append(dst, b)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		// The contract extends the RFC 8785 table with the three characters a
		// Unicode-aware line consumer would treat as a line boundary. They are
		// legal payload inside data strings and keys, so escaping them here is
		// what keeps the one-line envelope from depending on payload content.
		switch r {
		case '\u0085', '\u2028', '\u2029':
			dst = appendUnicodeEscape(dst, r)
		default:
			dst = append(dst, s[i:i+size]...)
		}
		i += size
	}
	return append(dst, '"')
}

// appendUnicodeEscape writes a BMP code point as lowercase \uXXXX.
func appendUnicodeEscape(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u',
		hexDigits[r>>12&0xf], hexDigits[r>>8&0xf], hexDigits[r>>4&0xf], hexDigits[r&0xf])
}
