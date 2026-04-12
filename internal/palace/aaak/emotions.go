// Package aaak implements the AAAK dialect — a 30x token compression format
// for wake-up context. Uses 3-char entity codes, emotion markers, flags,
// and topic extraction. Zero-API, pure regex/keyword.
package aaak

import "strings"

// emotionKeywords maps keywords to emotion codes.
// Each keyword triggers the corresponding emotion code when detected in text.
var emotionKeywords = map[string]string{
	// Vulnerability
	"vulnerable": "vul", "helpless": "vul", "exposed": "vul",
	// Joy
	"happy": "joy", "excited": "joy", "delighted": "joy", "thrilled": "joy",
	// Fear
	"scared": "fear", "afraid": "fear", "terrified": "fear", "worried": "fear",
	// Trust
	"trust": "trust", "reliable": "trust", "dependable": "trust", "confident": "trust",
	// Grief
	"grief": "grief", "mourning": "grief", "loss": "grief", "bereaved": "grief",
	// Wonder
	"wonder": "wonder", "amazed": "wonder", "astonished": "wonder", "incredible": "wonder",
	// Rage
	"rage": "rage", "furious": "rage", "angry": "rage", "outraged": "rage",
	// Love
	"love": "love", "adore": "love", "cherish": "love", "devoted": "love",
	// Hope
	"hope": "hope", "optimistic": "hope", "hopeful": "hope", "promising": "hope",
	// Despair
	"despair": "despair", "hopeless": "despair", "despondent": "despair",
	// Peace
	"peace": "peace", "calm": "peace", "serene": "peace", "tranquil": "peace",
	// Humor
	"funny": "humor", "hilarious": "humor", "amusing": "humor", "laughing": "humor",
	// Tenderness
	"tender": "tender", "gentle": "tender", "caring": "tender", "compassion": "tender",
	// Raw
	"raw": "raw", "unfiltered": "raw", "honest": "raw", "brutal": "raw",
	// Doubt
	"doubt": "doubt", "uncertain": "doubt", "skeptical": "doubt", "questionable": "doubt",
	// Relief
	"relief": "relief", "relieved": "relief", "resolved": "relief",
	// Anxiety
	"anxiety": "anx", "anxious": "anx", "nervous": "anx", "stressed": "anx",
	// Exhaustion
	"exhausted": "exhaust", "tired": "exhaust", "burned out": "exhaust", "drained": "exhaust",
	// Conviction
	"conviction": "convict", "determined": "convict", "resolute": "convict", "committed": "convict",
	// Passion
	"passion": "passion", "passionate": "passion", "fervent": "passion", "intense": "passion",
}

// DetectEmotions scans text for emotion keywords and returns unique emotion codes.
// The search is case-insensitive and matches whole word boundaries via space splitting.
func DetectEmotions(text string) []string {
	if text == "" {
		return nil
	}

	lower := strings.ToLower(text)
	seen := make(map[string]bool)
	var codes []string

	for keyword, code := range emotionKeywords {
		if strings.Contains(lower, keyword) && !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}

	return codes
}
