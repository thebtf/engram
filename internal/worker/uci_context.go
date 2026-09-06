package worker

import (
	"errors"
	"fmt"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

// uciContextComposition holds the one shared context authority used by MCP and
// private UCI gRPC calls for the lifetime of a worker.
type uciContextComposition struct {
	contextStore       *gormstore.UCIContextStore
	authorizer         *gormstore.UCIContextAuthorizer
	resolver           *uci.ContextResolver
	contextApplication *mcp.UCIContextApplication
	application        *UCIApplication
	projectionStore    *gormstore.UCIProjectionStore
	runtime            grpcserver.ContextAwareUCIRuntime
	handlePort         *mcp.UCIContextHandlePort
	aliasResolver      *uci.AliasResolver
	exposureRecorder   *uci.ExposureRecorder
	transport          grpcserver.UCITransport
}

// composeUCIContext creates and installs the narrow UCI context capability.
// The disabled path returns before it allocates or installs any UCI dependency.
func composeUCIContext(enabled bool, db *gormlib.DB, mcpServer *mcp.Server) (*uciContextComposition, error) {
	if !enabled {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("UCI context composition requires a database")
	}
	if mcpServer == nil {
		return nil, errors.New("UCI context composition requires an MCP server")
	}

	contextStore := gormstore.NewUCIContextStore(db)
	authorizer := gormstore.NewUCIContextAuthorizer(contextStore)
	resolver := uci.NewContextResolver(contextStore, authorizer, contextStore)
	contextApplication, err := mcp.NewUCIContextApplication(resolver, contextStore)
	if err != nil {
		return nil, fmt.Errorf("create UCI MCP context application: %w", err)
	}

	projectionStore := gormstore.NewUCIProjectionStore(db)
	publisher, err := projectionStore.Publisher(authorizer, uci.DefaultIndexPublicationLimits())
	if err != nil {
		return nil, fmt.Errorf("create UCI index publisher: %w", err)
	}
	runtime, err := grpcserver.NewContextAwareUCIRuntime(contextStore, projectionStore, publisher)
	if err != nil {
		return nil, fmt.Errorf("create UCI runtime: %w", err)
	}
	handlePort, err := mcp.NewUCIContextHandlePort(mcpServer, contextStore, authorizer)
	if err != nil {
		return nil, fmt.Errorf("create UCI context handle port: %w", err)
	}
	aliasResolver := uci.NewAliasResolver(contextStore.LookupLegacyAliasRecords)
	queryService := uci.NewQueryService(projectionStore)
	graphService := uci.NewGraphService(projectionStore)
	versionedReadService := uci.NewVersionedReadService(projectionStore)
	indexStatusService := uci.NewIndexStatusService(projectionStore)
	application, err := NewUCIApplication(
		contextApplication,
		aliasResolver,
		queryService,
		nil,
		graphService,
		versionedReadService,
		indexStatusService,
	)
	if err != nil {
		return nil, fmt.Errorf("create UCI application: %w", err)
	}
	exposureRecorder := uci.NewExposureRecorder(gormstore.NewUCIExposureStore(db), nil)
	transport := grpcserver.NewContextAwareUCITransport(resolver, aliasResolver, runtime, handlePort)

	mcpServer.SetCodebaseContextApplication(application)
	mcpServer.SetUCIExposureRecorder(exposureRecorder)

	return &uciContextComposition{
		contextStore:       contextStore,
		authorizer:         authorizer,
		resolver:           resolver,
		contextApplication: contextApplication,
		application:        application,
		projectionStore:    projectionStore,
		runtime:            runtime,
		handlePort:         handlePort,
		aliasResolver:      aliasResolver,
		exposureRecorder:   exposureRecorder,
		transport:          transport,
	}, nil
}
