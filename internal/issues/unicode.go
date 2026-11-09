package issues

import "strconv"

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Refuse them
// before decoding so an opaque key or admitted metadata is never rebound.
func validUnicodeEscapes(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(raw) {
				return false
			}
			if raw[index] != 'u' {
				continue
			}
			if index+4 >= len(raw) {
				return false
			}
			value, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
			if err != nil {
				return false
			}
			index += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			if value >= 0xd800 && value <= 0xdbff {
				if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			}
		}
	}
	return true
}
