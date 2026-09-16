package sessioncompat

import (
	"encoding/json"
	"time"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Payload is the exact legacy HTTP/OMP structured session-start shape shared
// by the HTTP compatibility route and the private daemon relay gateway.
type Payload struct {
	Issues      []map[string]any `json:"issues"`
	Rules       []map[string]any `json:"rules"`
	Memories    []map[string]any `json:"memories"`
	GeneratedAt string           `json:"generated_at"`
}

// FromResponse maps the authoritative gRPC response into the established
// compatibility JSON shape without adding project authority or relay metadata.
func FromResponse(response *pb.GetSessionStartContextResponse) Payload {
	if response == nil {
		return Payload{Issues: []map[string]any{}, Rules: []map[string]any{}, Memories: []map[string]any{}}
	}
	generatedAt := ""
	if timestamp := response.GetGeneratedAt(); timestamp != nil {
		generatedAt = timestamp.AsTime().UTC().Format(time.RFC3339)
	}
	return Payload{
		Issues:      issueMaps(response.GetIssues()),
		Rules:       ruleMaps(response.GetRules()),
		Memories:    memoryMaps(response.GetMemories()),
		GeneratedAt: generatedAt,
	}
}

// MarshalResponse returns the same bytes json.Marshal would produce for the
// HTTP compatibility payload. The extension remains the rendering owner.
func MarshalResponse(response *pb.GetSessionStartContextResponse) ([]byte, error) {
	return json.Marshal(FromResponse(response))
}

func issueMaps(issues []*pb.SessionStartIssue) []map[string]any {
	result := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		entry := map[string]any{
			"id":             issue.GetId(),
			"title":          issue.GetTitle(),
			"body":           issue.GetBody(),
			"status":         issue.GetStatus(),
			"priority":       issue.GetPriority(),
			"type":           issue.GetType(),
			"source_project": issue.GetSourceProject(),
			"target_project": issue.GetTargetProject(),
			"source_agent":   issue.GetSourceAgent(),
			"labels":         append([]string(nil), issue.GetLabels()...),
			"comment_count":  issue.GetCommentCount(),
		}
		addTimestamp(entry, "acknowledged_at", issue.GetAcknowledgedAt())
		addTimestamp(entry, "resolved_at", issue.GetResolvedAt())
		addTimestamp(entry, "reopened_at", issue.GetReopenedAt())
		addTimestamp(entry, "closed_at", issue.GetClosedAt())
		addTimestamp(entry, "created_at", issue.GetCreatedAt())
		addTimestamp(entry, "updated_at", issue.GetUpdatedAt())
		result = append(result, entry)
	}
	return result
}

func ruleMaps(rules []*pb.SessionStartRule) []map[string]any {
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		entry := map[string]any{
			"id":        rule.GetId(),
			"project":   rule.GetProject(),
			"content":   rule.GetContent(),
			"edited_by": rule.GetEditedBy(),
			"priority":  rule.GetPriority(),
			"version":   rule.GetVersion(),
			"narrative": rule.GetContent(),
			"title":     rule.GetContent(),
			"facts":     []string{},
		}
		addTimestamp(entry, "created_at", rule.GetCreatedAt())
		addTimestamp(entry, "updated_at", rule.GetUpdatedAt())
		result = append(result, entry)
	}
	return result
}

func memoryMaps(memories []*pb.SessionStartMemory) []map[string]any {
	result := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		if memory == nil {
			continue
		}
		entry := map[string]any{
			"id":           memory.GetId(),
			"project":      memory.GetProject(),
			"content":      memory.GetContent(),
			"tags":         append([]string(nil), memory.GetTags()...),
			"source_agent": memory.GetSourceAgent(),
			"edited_by":    memory.GetEditedBy(),
			"version":      memory.GetVersion(),
		}
		addTimestamp(entry, "created_at", memory.GetCreatedAt())
		addTimestamp(entry, "updated_at", memory.GetUpdatedAt())
		result = append(result, entry)
	}
	return result
}

func addTimestamp(target map[string]any, key string, timestamp *timestamppb.Timestamp) {
	if timestamp != nil {
		target[key] = timestamp.AsTime().UTC().Format(time.RFC3339)
	}
}
