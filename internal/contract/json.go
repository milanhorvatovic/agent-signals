package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth bounds JSON nesting, objects and arrays combined with the root at
// depth 1 (event-contract.md §Event). It is enforced while decoding, before
// schema validation, so a byte-valid line cannot drive this decoder or the
// canonicalizer past its stack.
const MaxDepth = 64

// decodeStrict parses exactly one JSON document per the parse profile: the
// input is valid UTF-8 carrying no unpaired surrogate, nesting stays within
// MaxDepth, duplicate object keys are rejected at every depth, number lexemes
// are preserved as json.Number, and trailing content after the document is an
// error. Values come back as map[string]any, []any, string, json.Number,
// bool, or nil.
func decodeStrict(data []byte) (any, error) {
	// Both text checks read the raw bytes because decoding destroys the
	// evidence: encoding/json turns a surrogate half — encoded or escaped —
	// into U+FFFD, after which the input is indistinguishable from a line
	// that legitimately carried a replacement character.
	if !utf8.Valid(data) {
		return nil, errors.New("input is not valid UTF-8")
	}
	if err := rejectEscapedSurrogates(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec, 1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON document")
	}
	return v, nil
}

// rejectEscapedSurrogates reports a \uXXXX escape that does not denote a
// Unicode scalar value: a high surrogate not followed by the escape of a low
// one, or a low surrogate standing alone. Every accepted string is therefore a
// sequence of scalars, which is what makes the canonical raw-UTF-8 emission
// defined for all accepted input (event-contract.md §Spool and cursors).
//
// A malformed escape is left to the decoder rather than reported here: this
// scan is looking for one specific well-formed construct, and the decoder's
// message for a truncated escape names the offset.
func rejectEscapedSurrogates(data []byte) error {
	inString := false
	for i := 0; i < len(data); {
		switch b := data[i]; {
		case !inString:
			inString = b == '"'
			i++
		case b == '"':
			inString = false
			i++
		case b != '\\':
			i++
		case i+1 == len(data) || data[i+1] != 'u':
			// Any other escape is two bytes, and consuming both is what keeps
			// the second backslash of \\ from opening an escape of its own.
			i += 2
		default:
			unit, ok := hexEscape(data[i:])
			if !ok {
				return nil
			}
			if !utf16.IsSurrogate(rune(unit)) {
				i += 6
				continue
			}
			low, ok := hexEscape(data[i+6:])
			if !ok || utf16.DecodeRune(rune(unit), rune(low)) == utf8.RuneError {
				return fmt.Errorf(`unpaired surrogate escape \u%04x`, unit)
			}
			i += 12
		}
	}
	return nil
}

// hexEscape reads a \uXXXX escape at the head of data.
func hexEscape(data []byte) (uint16, bool) {
	if len(data) < 6 || data[0] != '\\' || data[1] != 'u' {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(data[2:6]), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(unit), true
}

// decodeValue decodes one value. depth is the nesting level this value would
// occupy if it turns out to be a container.
func decodeValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth > MaxDepth {
		return nil, fmt.Errorf("JSON nesting is deeper than the %d-level cap", MaxDepth)
	}
	switch delim {
	case '{':
		return decodeObject(dec, depth)
	case '[':
		return decodeArray(dec, depth)
	default:
		return nil, fmt.Errorf("unexpected %q", delim)
	}
}

func decodeObject(dec *json.Decoder, depth int) (map[string]any, error) {
	obj := make(map[string]any)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is %T, not string", keyTok)
		}
		if _, exists := obj[key]; exists {
			return nil, fmt.Errorf("duplicate object key %q", key)
		}
		val, err := decodeValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		obj[key] = val
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return obj, nil
}

func decodeArray(dec *json.Decoder, depth int) ([]any, error) {
	arr := []any{}
	for dec.More() {
		val, err := decodeValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, err
	}
	return arr, nil
}
