package aaak

import "strings"

// Flag represents a content classification flag.
type Flag string

const (
	FlagOrigin    Flag = "ORIGIN"
	FlagCore      Flag = "CORE"
	FlagSensitive Flag = "SENSITIVE"
	FlagPivot     Flag = "PIVOT"
	FlagGenesis   Flag = "GENESIS"
	FlagDecision  Flag = "DECISION"
	FlagTechnical Flag = "TECHNICAL"
)

// flagKeywords maps keywords to flags. Multiple keywords can trigger the same flag.
var flagKeywords = map[string]Flag{
	// ORIGIN — foundational, beginning
	"origin": FlagOrigin, "beginning": FlagOrigin, "started": FlagOrigin,
	"first time": FlagOrigin, "initial": FlagOrigin,
	// CORE — fundamental identity/values
	"core": FlagCore, "fundamental": FlagCore, "essential": FlagCore,
	"identity": FlagCore, "who i am": FlagCore, "values": FlagCore,
	// SENSITIVE — private, vulnerable content
	"sensitive": FlagSensitive, "private": FlagSensitive, "secret": FlagSensitive,
	"confidential": FlagSensitive, "personal": FlagSensitive,
	// PIVOT — turning point, major change
	"turning point": FlagPivot, "pivot": FlagPivot, "breakthrough": FlagPivot,
	"changed everything": FlagPivot, "game changer": FlagPivot,
	// GENESIS — creation, new project/idea
	"genesis": FlagGenesis, "created": FlagGenesis, "invented": FlagGenesis,
	"new project": FlagGenesis, "launched": FlagGenesis,
	// DECISION — explicit choice
	"decided": FlagDecision, "decision": FlagDecision, "chose": FlagDecision,
	"selected": FlagDecision, "opted": FlagDecision,
	// TECHNICAL — architecture, code, infrastructure
	"architecture": FlagTechnical, "technical": FlagTechnical, "infrastructure": FlagTechnical,
	"database": FlagTechnical, "api": FlagTechnical, "server": FlagTechnical,
	"deploy": FlagTechnical, "migration": FlagTechnical,
}

// DetectFlags scans text for flag keywords and returns unique flags.
func DetectFlags(text string) []string {
	if text == "" {
		return nil
	}

	lower := strings.ToLower(text)
	seen := make(map[Flag]bool)
	var flags []string

	for keyword, flag := range flagKeywords {
		if strings.Contains(lower, keyword) && !seen[flag] {
			seen[flag] = true
			flags = append(flags, string(flag))
		}
	}

	return flags
}
