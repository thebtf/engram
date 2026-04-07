package aaak

import (
	"context"
	"fmt"
	cryptorand "crypto/rand"
	"encoding/binary"
	"strings"

	"gorm.io/gorm"
)

// GenerateCode creates a 3-char uppercase code for an entity name.
// If the natural code (first 3 chars) is taken, tries name[:2]+digit,
// then random 3-char codes.
func GenerateCode(name string, existing map[string]bool) string {
	if name == "" {
		return "UNK"
	}

	// Normalize: uppercase, letters only
	clean := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return -1
	}, name)

	if len(clean) < 2 {
		clean = clean + "XX"
	}

	// Attempt 1: first 3 chars
	if len(clean) >= 3 {
		code := clean[:3]
		if !existing[code] {
			return code
		}
	}

	// Attempt 2: first 2 chars + digit (2-9)
	prefix := clean[:2]
	for d := 2; d <= 9; d++ {
		code := fmt.Sprintf("%s%d", prefix, d)
		if !existing[code] {
			return code
		}
	}

	// Attempt 3: random 3-char uppercase codes (crypto/rand for uniqueness)
	for i := 0; i < 100; i++ {
		var buf [2]byte
		_, _ = cryptorand.Read(buf[:])
		n := binary.LittleEndian.Uint16(buf[:])
		code := string([]byte{
			byte('A' + n%26),
			byte('A' + (n/26)%26),
			byte('A' + (n/676)%26),
		})
		if !existing[code] {
			return code
		}
	}

	// Fallback (should never reach with 17576 possible codes)
	return clean[:3]
}

// LookupCodes reads AAAK codes from entity observation metadata in the database.
// Returns a map of lowercase entity name → 3-char code.
func LookupCodes(ctx context.Context, db *gorm.DB, project string) (map[string]string, error) {
	if db == nil {
		return make(map[string]string), nil
	}

	var rows []struct {
		Title     string
		Narrative string
	}

	q := db.WithContext(ctx).
		Table("observations").
		Select("title, COALESCE(narrative, '') as narrative").
		Where("type = 'entity' AND is_superseded = 0 AND is_archived = 0")

	if project != "" {
		q = q.Where("project = ?", project)
	}

	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lookup aaak codes: %w", err)
	}

	codes := make(map[string]string, len(rows))
	for _, row := range rows {
		name := strings.ToLower(strings.TrimSpace(row.Title))
		if name == "" {
			continue
		}
		// Extract aaak_code from narrative JSON if present
		// Narrative contains EntityMetadata JSON — look for "aaak_code":"XXX"
		if idx := strings.Index(row.Narrative, `"aaak_code":"`); idx >= 0 {
			start := idx + len(`"aaak_code":"`)
			end := strings.Index(row.Narrative[start:], `"`)
			if end > 0 && end <= 5 {
				codes[name] = row.Narrative[start : start+end]
			}
		}
	}

	return codes, nil
}
