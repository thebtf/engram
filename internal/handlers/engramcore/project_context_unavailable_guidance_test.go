package engramcore

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestProjectContextUnavailableGuidanceHoldsUntilDependencyRestored(t *testing.T) {
	for _, action := range []string{"choices", "select"} {
		for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
			t.Run(action+"/"+http.StatusText(status), func(t *testing.T) {
				fixture := newProjectChoiceFixture(t, false)
				choices := projectChoiceList(t, fixture)
				before := projectChoiceSnapshot(t, fixture)
				fixture.backend.catalogStatus = status
				args := json.RawMessage(`{"action":"choices"}`)
				if action == "select" {
					args, _ = json.Marshal(map[string]string{"action": action, "selection_ref": choices[1].SelectionRef})
				}
				reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "project_context", args)
				require.True(t, reply.failed())
				var rendered []string
				if reply.Error != nil {
					rendered = append(rendered, reply.Error.Message)
				}
				for _, block := range reply.Result.Content {
					rendered = append(rendered, block.Text)
				}
				message := strings.ToLower(strings.Join(rendered, "\n"))
				require.NotEmpty(t, message, "the actual refusal must be visible to the caller")
				assert.Contains(t, message, "project_resolution_unavailable")
				assert.Regexp(t, `(connect|server|service|dependenc|authori[sz]|permission)`, message)
				assert.Regexp(t, `(no select[^.\n]{0,40}attempted)[^.\n]{0,100}(known not sent)[^.\n]{0,100}fresh authorized choices`, message, "a read-only failure or selection known not sent must not require an existing binding")
				assert.Regexp(t, `(unconfirmed|unknown)[^.\n]{0,100}(selection|select)|(selection|select)[^.\n]{0,100}(unconfirmed|unknown)`, message)
				condition := regexp.MustCompile(`(after|once|until|when)[^.\n]{0,180}\b(restored|restoration|recovered|recovery|reestablished|reestablishment|available again)\b`).FindStringIndex(message)
				if assert.NotNil(t, condition, "re-entry must wait for restored connection or authorization") {
					if action == "choices" {
						assert.Greater(t, strings.Index(message, "fresh authorized choices"), condition[1], "read-only choices may re-enter only after restoration")
					}
					immediate := regexp.MustCompile(`\b(call|use|refresh|retry)\b[^.\n]*(choices|select)`)
					negative := regexp.MustCompile(`\b(do not|don't|never|avoid|hold)\b`)
					for _, sentence := range regexp.MustCompile(`[.\n]`).Split(message[:condition[0]], -1) {
						if immediate.MatchString(sentence) {
							assert.Regexp(t, negative, sentence, "do not direct an immediate choices/select loop before restoration")
						}
					}
				}
				writeGuard := false
				for _, sentence := range regexp.MustCompile(`[.\n]`).Split(message, -1) {
					if regexp.MustCompile(`(unknown|unconfirmed)`).MatchString(sentence) && regexp.MustCompile(`(write|mutation)`).MatchString(sentence) && regexp.MustCompile(`(inspect|verify|check|reconcile)`).MatchString(sentence) && regexp.MustCompile(`(before|first|prior)`).MatchString(sentence) && regexp.MustCompile(`retr(y|ying)`).MatchString(sentence) {
						writeGuard = true
					}
				}
				assert.True(t, writeGuard, "inspect an unknown original write outcome before retry")
				if action == "select" {
					inspection := regexp.MustCompile(`(inspect|verify|check|reconcile)[^.\n]{0,160}(binding|selection|selected)|(binding|selection|selected)[^.\n]{0,160}(inspect|verify|check|reconcile)`).FindStringIndex(message)
					if assert.NotNil(t, inspection, "inspect the existing caller binding or selection outcome") {
						immediate := regexp.MustCompile(`\b(call|use|refresh|retry)\b[^.\n]*(choices|select)`)
						negative := regexp.MustCompile(`\b(do not|don't|never|avoid|hold|before)\b`)
						for _, sentence := range regexp.MustCompile(`[.\n]`).Split(message[:inspection[0]], -1) {
							if immediate.MatchString(sentence) {
								assert.Regexp(t, negative, sentence, "do not retry the selection before inspecting its unconfirmed outcome")
							}
						}
					}
					assert.Regexp(t, `(inspect|verify|check|reconcile)[^.\n]{0,160}(binding|selection|selected)[^.\n]{0,160}(before|first|prior)[^.\n]{0,100}retr(y|ying)|(before|prior)[^.\n]{0,160}retr(y|ying)[^.\n]{0,160}(inspect|verify|check|reconcile)[^.\n]{0,160}(binding|selection|selected)|first[^.\n]{0,100}(inspect|verify|check|reconcile)[^.\n]{0,160}(binding|selection|selected)`, message, "reconcile an unconfirmed selection before retry")
				}
				for _, term := range []string{"original", "target", "actor", "session", "client"} {
					assert.Contains(t, message, term)
				}
				assert.NotContains(t, message, choices[1].SelectionRef)
				assert.Equal(t, before, projectChoiceSnapshot(t, fixture))
				projectChoiceAssertNoForwards(t, fixture.backend)

				fixture.backend.catalogStatus = http.StatusOK
				fresh := projectChoiceList(t, fixture)
				require.False(t, projectChoiceSelect(t, fixture, fresh[1].SelectionRef).failed(), "a fresh authorized choice is the supported bounded re-entry")
				projectChoiceAssertNoForwards(t, fixture.backend)
			})
		}
	}
}

func TestProjectContextUnavailableGuidanceDoesNotReplaceSelectionRecovery(t *testing.T) {
	for _, code := range []string{"PROJECT_MIGRATION_REQUIRED", "PROJECT_IDENTITY_CONFLICT", "PROJECT_SELECTION_INVALID", "PROJECT_SELECTION_STALE"} {
		t.Run(code, func(t *testing.T) {
			guidance := projectidentity.RecoveryGuidance(code)
			assert.Contains(t, guidance, `action="choices"`)
			assert.Contains(t, guidance, `action="select"`)
			assert.Contains(t, guidance, "selection_ref")
		})
	}
}
