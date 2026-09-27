package uci

// NoAuthCode* names a server-selected technical UCI scope. The realm is not
// the legacy auth-disabled memory realm or any authenticated keycard realm.
const (
	NoAuthCodeRealm       = "local-code-v3"
	NoAuthCodePrincipal   = "service/local-code-v3"
	NoAuthCodeWorkstation = "local-code-v3"
)
