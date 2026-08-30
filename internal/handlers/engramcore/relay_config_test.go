package engramcore

import (
	"testing"

	"github.com/thebtf/engram/internal/config"
)

const (
	testRelayProject = "11111111-1111-4111-8111-111111111111"
	testRegistration = "engram_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testProjectToken = "engram_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func validRelayEnv() map[string]string {
	return map[string]string{
		config.EnvServerURL:               "http://127.0.0.1:37777",
		config.EnvHAP01BRegistrationToken: testRegistration,
		config.EnvHAP01BProjectTokensJSON: `{"` + testRelayProject + `":"` + testProjectToken + `"}`,
	}
}

func TestParseRelayChildConfigSeparatesRegistrationFromData(t *testing.T) {
	parsed, err := parseRelayChildConfig(validRelayEnv(), true)
	if err != nil {
		t.Fatalf("parse registration config: %v", err)
	}
	if parsed.registrationToken != testRegistration || parsed.projectTokens[testRelayProject] != testProjectToken {
		t.Fatalf("parsed config = %#v", parsed)
	}

	withoutRegistration := validRelayEnv()
	delete(withoutRegistration, config.EnvHAP01BRegistrationToken)
	if _, err := parseRelayChildConfig(withoutRegistration, true); err == nil {
		t.Fatal("registration config accepted missing registration keycard")
	}
	if _, err := parseRelayChildConfig(withoutRegistration, false); err != nil {
		t.Fatalf("data config rejected without registration keycard: %v", err)
	}
}

func TestParseRelayChildConfigRejectsAmbiguousOrUnsafeInputs(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{name: "URL userinfo", env: func() map[string]string {
			env := validRelayEnv()
			env[config.EnvServerURL] = "https://user:secret@example.test"
			return env
		}()},
		{name: "duplicate project", env: func() map[string]string {
			env := validRelayEnv()
			env[config.EnvHAP01BProjectTokensJSON] = `{"` + testRelayProject + `":"` + testProjectToken + `","` + testRelayProject + `":"` + testProjectToken + `"}`
			return env
		}()},
		{name: "noncanonical project", env: func() map[string]string {
			env := validRelayEnv()
			env[config.EnvHAP01BProjectTokensJSON] = `{"NOT-A-UUID":"` + testProjectToken + `"}`
			return env
		}()},
		{name: "invalid keycard", env: func() map[string]string {
			env := validRelayEnv()
			env[config.EnvHAP01BProjectTokensJSON] = `{"` + testRelayProject + `":"not-a-keycard"}`
			return env
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseRelayChildConfig(test.env, true); err == nil {
				t.Fatal("unsafe child config was accepted")
			}
		})
	}
}

func TestRelayCredentialRefChangesWithEveryAuthorityAxis(t *testing.T) {
	base, err := relayCredentialRef("http://127.0.0.1:37777", testRelayProject, testProjectToken)
	if err != nil {
		t.Fatalf("base credential ref: %v", err)
	}
	changedToken, _ := relayCredentialRef("http://127.0.0.1:37777", testRelayProject, "engram_cccccccccccccccccccccccccccccccc")
	changedURL, _ := relayCredentialRef("https://127.0.0.1:37777", testRelayProject, testProjectToken)
	changedProject, _ := relayCredentialRef("http://127.0.0.1:37777", "22222222-2222-4222-8222-222222222222", testProjectToken)
	if base == changedToken || base == changedURL || base == changedProject {
		t.Fatal("credential reference failed to bind token, endpoint, and project")
	}
}
