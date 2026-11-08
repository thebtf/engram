package gorm

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ValidIssueSessionSelector admits exact opaque host session identifiers. An
// empty selector means omitted; an invalid nonempty selector must not broaden a query.
func ValidIssueSessionSelector(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	if id, err := uuid.Parse(value); err == nil && id == uuid.Nil {
		return false
	}
	return true
}
