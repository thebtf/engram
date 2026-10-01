package uci

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NoAuthCode* names a server-selected technical UCI scope. The realm is not
// the legacy auth-disabled memory realm or any authenticated keycard realm.
const (
	NoAuthCodeRealm                     = "local-code-v3"
	NoAuthCodePrincipal                 = "service/local-code-v3"
	NoAuthCodeWorkstation               = "local-code-v3"
	NoAuthCodeClientInstanceMetadataKey = "x-engram-uci-client-instance-id-bin"
)

func NoAuthCodeWorkstationForInstance(instance string) (string, bool) {
	if !ValidCodeClientInstanceID(instance) {
		return "", false
	}
	fingerprint := sha256.Sum256([]byte("engram/noauth-code/workstation/v1\x00" + instance))
	return fmt.Sprintf("noauth-code-%x", fingerprint), true
}

// ValidCodeClientInstanceID applies the V3 opaque installation-ID boundary
// without deriving a workstation identity.
func ValidCodeClientInstanceID(instance string) bool {
	if len(instance) == 0 || !utf8.ValidString(instance) || strings.TrimSpace(instance) != instance || strings.ContainsAny(instance, "/\\@") {
		return false
	}
	runeCount := 0
	for _, character := range instance {
		runeCount++
		if runeCount > 256 || unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	if (instance[0] >= 'a' && instance[0] <= 'z') || (instance[0] >= 'A' && instance[0] <= 'Z') {
		for index := 1; index < len(instance); index++ {
			character := instance[index]
			if character == ':' {
				return false
			}
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '+' || character == '.' || character == '-') {
				break
			}
		}
	}
	return true
}
