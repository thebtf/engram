package engramcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/legacyrelay"
)

const (
	maxRelayProjectCredentials = 256
	maxRelayCredentialMapBytes = 64 * 1024
)

type relayChildConfig struct {
	serverURL         string
	registrationToken string
	projectTokens     map[string]string
}

func parseRelayChildConfig(env map[string]string, requireRegistration bool) (relayChildConfig, error) {
	serverURL := strings.TrimSpace(env[config.EnvServerURL])
	if serverURL == "" {
		serverURL = strings.TrimSpace(env[config.EnvServerURLAlt])
	}
	if err := validateRelayServerURL(serverURL); err != nil {
		return relayChildConfig{}, err
	}
	registrationToken := strings.TrimSpace(env[config.EnvHAP01BRegistrationToken])
	if requireRegistration && !validRelayKeycard(registrationToken) {
		return relayChildConfig{}, errors.New("relay registration keycard is unavailable")
	}
	projectTokens, err := parseRelayProjectTokens(env[config.EnvHAP01BProjectTokensJSON])
	if err != nil {
		return relayChildConfig{}, err
	}
	return relayChildConfig{serverURL: serverURL, registrationToken: registrationToken, projectTokens: projectTokens}, nil
}

func validateRelayServerURL(raw string) error {
	if raw == "" || len(raw) > 2048 {
		return errors.New("relay server URL is unavailable")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("relay server URL is invalid")
	}
	return nil
}

func parseRelayProjectTokens(raw string) (map[string]string, error) {
	if raw == "" || len(raw) > maxRelayCredentialMapBytes {
		return nil, errors.New("relay project keycard map is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("relay project keycard map is invalid")
	}
	result := make(map[string]string)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("relay project keycard map is invalid")
		}
		project, ok := keyToken.(string)
		if !ok || len(result) >= maxRelayProjectCredentials {
			return nil, errors.New("relay project keycard map is invalid")
		}
		if _, duplicate := result[project]; duplicate {
			return nil, errors.New("relay project keycard map has duplicate project")
		}
		parsedProject, err := uuid.Parse(project)
		if err != nil || parsedProject.String() != project {
			return nil, errors.New("relay project keycard map has invalid canonical project")
		}
		var keycard string
		if err := decoder.Decode(&keycard); err != nil || !validRelayKeycard(keycard) {
			return nil, errors.New("relay project keycard map has invalid keycard")
		}
		result[project] = keycard
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("relay project keycard map is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("relay project keycard map has trailing data")
	}
	if len(result) == 0 {
		return nil, errors.New("relay project keycard map is empty")
	}
	return result, nil
}

func validRelayKeycard(raw string) bool {
	if !strings.HasPrefix(raw, auth.TokenRawPrefix) || len(raw) != auth.TokenTotalLen {
		return false
	}
	_, err := hex.DecodeString(raw[len(auth.TokenRawPrefix):])
	return err == nil
}

func relayCredentialRef(serverURL, canonicalProject, token string) (legacyrelay.CredentialRef, error) {
	if err := validateRelayServerURL(serverURL); err != nil || canonicalProject == "" || !validRelayKeycard(token) {
		return legacyrelay.CredentialRef{}, errors.New("relay credential reference inputs are invalid")
	}
	digest := sha256.Sum256([]byte("engram.hap01b.credential/v1\x00" + serverURL + "\x00" + canonicalProject + "\x00" + token))
	return legacyrelay.NewCredentialRef("sha256-" + fmt.Sprintf("%x", digest[:]))
}
