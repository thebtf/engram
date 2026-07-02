package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type graphEdgeWriteStore interface {
	Create(ctx context.Context, e *graph.Edge) (*graph.Edge, error)
	ListByMemory(ctx context.Context, memoryID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
	ListByNode(ctx context.Context, nodeID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
}

type graphMemoryGetter interface {
	Get(ctx context.Context, id int64) (*models.Memory, error)
}

func graphCreateEdgeWithGuards(ctx context.Context, edgeStore graphEdgeWriteStore, nodeStore nodesStoreAPI, memoryStore graphMemoryGetter, edge *graph.Edge) (*graph.Edge, error) {
	if edgeStore == nil {
		return nil, fmt.Errorf("graph store not available")
	}

	unlock := graph.LockWrites()
	defer unlock()

	sourceType := edge.SourceType
	if sourceType == "" {
		sourceType = "memory"
	}
	targetType := edge.TargetType
	if targetType == "" {
		targetType = "memory"
	}

	var sourceMemoryID, targetMemoryID, sourceNodeID, targetNodeID int64
	if edge.SourceID != nil {
		sourceMemoryID = *edge.SourceID
	}
	if edge.TargetID != nil {
		targetMemoryID = *edge.TargetID
	}
	if edge.NodeSourceID != nil {
		sourceNodeID = *edge.NodeSourceID
	}
	if edge.NodeTargetID != nil {
		targetNodeID = *edge.NodeTargetID
	}

	exists, err := graphEndpointExistsWithGuards(ctx, sourceType, sourceMemoryID, sourceNodeID, memoryStore, nodeStore)
	if err != nil {
		return nil, fmt.Errorf("source endpoint validation: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("orphan_edge: source endpoint does not exist")
	}
	exists, err = graphEndpointExistsWithGuards(ctx, targetType, targetMemoryID, targetNodeID, memoryStore, nodeStore)
	if err != nil {
		return nil, fmt.Errorf("target endpoint validation: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("orphan_edge: target endpoint does not exist")
	}

	duplicate, err := graphDuplicateEdgeExists(ctx, edgeStore, *edge)
	if err != nil {
		return nil, err
	}
	if duplicate {
		return nil, fmt.Errorf("duplicate_edge: an active edge with the same source, target, and type already exists")
	}

	return edgeStore.Create(ctx, edge)
}

func graphEndpointExistsWithGuards(ctx context.Context, endpointType string, memoryID, nodeID int64, memoryStore graphMemoryGetter, nodeStore nodesStoreAPI) (bool, error) {
	if endpointType == "node" {
		if nodeStore == nil {
			return false, fmt.Errorf("graph nodes store not available")
		}
		if nodeID == 0 {
			return false, nil
		}
		_, err := nodeStore.Get(ctx, nodeID, true)
		if err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	if memoryStore == nil {
		return false, fmt.Errorf("memory store not available")
	}
	if memoryID == 0 {
		return false, nil
	}
	_, err := memoryStore.Get(ctx, memoryID)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func graphDuplicateEdgeExists(ctx context.Context, edgeStore graphEdgeWriteStore, candidate graph.Edge) (bool, error) {
	sourceType := candidate.SourceType
	if sourceType == "" {
		sourceType = "memory"
	}
	targetType := candidate.TargetType
	if targetType == "" {
		targetType = "memory"
	}

	var existing []graph.Edge
	var err error
	if sourceType == "node" {
		if candidate.NodeSourceID == nil {
			return false, fmt.Errorf("node_source_id required when source_type='node'")
		}
		existing, err = edgeStore.ListByNode(ctx, *candidate.NodeSourceID, graph.Outgoing, candidate.EdgeType)
	} else {
		if candidate.SourceID == nil {
			return false, fmt.Errorf("source_id required when source_type='memory'")
		}
		existing, err = edgeStore.ListByMemory(ctx, *candidate.SourceID, graph.Outgoing, candidate.EdgeType)
	}
	if err != nil {
		return false, err
	}

	for _, edge := range existing {
		edgeSourceType := edge.SourceType
		if edgeSourceType == "" {
			edgeSourceType = "memory"
		}
		edgeTargetType := edge.TargetType
		if edgeTargetType == "" {
			edgeTargetType = "memory"
		}
		if edge.EdgeType != candidate.EdgeType || edgeSourceType != sourceType || edgeTargetType != targetType {
			continue
		}
		if sourceType == "node" {
			if edge.NodeSourceID == nil || candidate.NodeSourceID == nil || *edge.NodeSourceID != *candidate.NodeSourceID {
				continue
			}
		} else if edge.SourceID == nil || candidate.SourceID == nil || *edge.SourceID != *candidate.SourceID {
			continue
		}
		if targetType == "node" {
			if edge.NodeTargetID == nil || candidate.NodeTargetID == nil || *edge.NodeTargetID != *candidate.NodeTargetID {
				continue
			}
		} else if edge.TargetID == nil || candidate.TargetID == nil || *edge.TargetID != *candidate.TargetID {
			continue
		}
		return true, nil
	}
	return false, nil
}
