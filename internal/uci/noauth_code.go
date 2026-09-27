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
	NoAuthCodeClientInstanceMetadataKey = "x-engram-uci-client-instance-id"
)

func NoAuthCodeWorkstationForInstance(instance string) (string, bool) {
	if len(instance) == 0 || len(instance) > 256 || !utf8.ValidString(instance) || strings.TrimSpace(instance) != instance || strings.ContainsAny(instance, "/\\@:") {
		return "", false
	}
	for _, character := range instance {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", false
		}
	}
	fingerprint := sha256.Sum256([]byte("engram/noauth-code/workstation/v1\x00" + instance))
	return fmt.Sprintf("noauth-code-%x", fingerprint), true
}
