package uci

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	contextResolverTestRealm     = "uci-test-realm"
	contextResolverTestPrincipal = "uci-test-principal"
	contextResolverTestClientA   = "uci-test-client-a"
	contextResolverTestClientB   = "uci-test-client-b"
	contextResolverTestSpaceA    = "10000000-0000-4000-8000-000000000001"
	contextResolverTestSourceA   = "20000000-0000-4000-8000-000000000001"
	contextResolverTestSourceB   = "20000000-0000-4000-8000-000000000002"
	contextResolverTestCheckoutA = "30000000-0000-4000-8000-000000000001"
	contextResolverTestCheckoutB = "30000000-0000-4000-8000-000000000002"
	contextResolverTestViewA     = "40000000-0000-4000-8000-000000000001"
	contextResolverTestViewB     = "40000000-0000-4000-8000-000000000002"
	contextResolverTestProfile   = "50000000-0000-4000-8000-000000000001"
)

func TestUCIContextResolverRejectsInvalidOrMismatchedContextRefs(t *testing.T) {
	canonical := contextResolverTestRefA()
	withoutSpace := canonical
	withoutSpace.SpaceID = nil
	missingSource := canonical
	missingSource.SourceID = ""
	missingCheckout := canonical
	missingCheckout.CheckoutID = ""
	zeroGeneration := canonical
	zeroGeneration.Generation = 0
	mismatched := canonical
	mismatched.SourceID = contextResolverTestSourceB

	for _, test := range []struct {
		name                 string
		ref                  ContextRef
		catalogFailures      []contextResolverCatalogFailure
		catalogRecords       []contextResolverCatalogRecord
		wantCode             string
		wantRef              *ContextRef
		wantAuthorizerCalls  int
		wantCatalogCallCount int
	}{
		{
			name:                 "missing source",
			ref:                  missingSource,
			wantCode:             "CONTEXT_MISMATCH",
			wantAuthorizerCalls:  0,
			wantCatalogCallCount: 0,
		},
		{
			name:                 "missing checkout",
			ref:                  missingCheckout,
			wantCode:             "CONTEXT_MISMATCH",
			wantAuthorizerCalls:  0,
			wantCatalogCallCount: 0,
		},
		{
			name:                 "zero generation",
			ref:                  zeroGeneration,
			wantCode:             "CONTEXT_MISMATCH",
			wantAuthorizerCalls:  0,
			wantCatalogCallCount: 0,
		},
		{
			name: "source checkout view relationship mismatch",
			ref:  mismatched,
			catalogFailures: []contextResolverCatalogFailure{{
				ref: mismatched,
				err: errors.New("source 20000000-0000-4000-8000-000000000002 does not own checkout 30000000-0000-4000-8000-000000000001 at C:\\private\\worktree"),
			}},
			wantCode:             "CONTEXT_MISMATCH",
			wantAuthorizerCalls:  0,
			wantCatalogCallCount: 1,
		},
		{
			name: "catalog canonical tuple mismatch",
			ref:  mismatched,
			catalogRecords: []contextResolverCatalogRecord{{
				lookup: mismatched,
				record: ContextRecord{Ref: canonical, AuthRealm: contextResolverTestRealm},
			}},
			wantCode:             "CONTEXT_MISMATCH",
			wantAuthorizerCalls:  0,
			wantCatalogCallCount: 1,
		},
		{
			name:                 "space omitted",
			ref:                  withoutSpace,
			wantRef:              &withoutSpace,
			wantAuthorizerCalls:  1,
			wantCatalogCallCount: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &contextResolverCatalogFake{failures: test.catalogFailures, records: test.catalogRecords}
			authorizer := &contextResolverAuthorizerFake{}
			resolver := NewContextResolver(catalog, authorizer)
			ref := test.ref

			resolved, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil))
			if test.wantCode != "" {
				assertContextResolverCode(t, err, test.wantCode)
			} else {
				if err != nil {
					t.Fatalf("Resolve() error = %v", err)
				}
				assertContextResolverRef(t, resolved.Ref(), *test.wantRef)
			}
			if authorizer.calls != test.wantAuthorizerCalls {
				t.Fatalf("authorizer calls = %d, want %d", authorizer.calls, test.wantAuthorizerCalls)
			}
			if len(catalog.calls) != test.wantCatalogCallCount {
				t.Fatalf("catalog calls = %d, want %d", len(catalog.calls), test.wantCatalogCallCount)
			}
		})
	}
}

func TestUCIContextResolverRequiresExactlyOneCandidateWithoutBinding(t *testing.T) {
	candidateA := contextResolverTestRefA()
	candidateB := contextResolverTestRefB()

	for _, test := range []struct {
		name        string
		candidates  []ContextRef
		wantCode    string
		wantRef     *ContextRef
		wantCatalog []ContextRef
	}{
		{
			name:     "no candidates",
			wantCode: "CONTEXT_REQUIRED",
		},
		{
			name:       "multiple candidates",
			candidates: []ContextRef{candidateA, candidateB},
			wantCode:   "CONTEXT_REQUIRED",
		},
		{
			name:        "one candidate",
			candidates:  []ContextRef{candidateA},
			wantRef:     &candidateA,
			wantCatalog: []ContextRef{candidateA},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &contextResolverCatalogFake{}
			authorizer := &contextResolverAuthorizerFake{}
			resolver := NewContextResolver(catalog, authorizer)

			resolved, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, test.candidates))
			if test.wantCode != "" {
				assertContextResolverCode(t, err, test.wantCode)
			} else {
				if err != nil {
					t.Fatalf("Resolve() error = %v", err)
				}
				assertContextResolverRef(t, resolved.Ref(), *test.wantRef)
			}
			assertContextResolverCatalogCalls(t, catalog, test.wantCatalog...)
			if authorizer.calls != len(test.wantCatalog) {
				t.Fatalf("authorizer calls = %d, want %d", authorizer.calls, len(test.wantCatalog))
			}
		})
	}
}

func TestUCIContextResolverBindsExplicitAuthorizedContext(t *testing.T) {
	ref := contextResolverTestRefA()
	catalog := &contextResolverCatalogFake{}
	authorizer := &contextResolverAuthorizerFake{}
	resolver := NewContextResolver(catalog, authorizer)

	bound, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil))
	if err != nil {
		t.Fatalf("explicit Resolve() error = %v", err)
	}
	assertContextResolverRef(t, bound.Ref(), ref)

	reused, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, nil))
	if err != nil {
		t.Fatalf("bound Resolve() error = %v", err)
	}
	assertContextResolverRef(t, reused.Ref(), ref)
	assertContextResolverCatalogCalls(t, catalog, ref, ref)
	if authorizer.calls != 2 {
		t.Fatalf("authorizer calls = %d, want 2", authorizer.calls)
	}
	wantAccess := contextResolverTestAccess(contextResolverTestPrincipal, ref)
	for index, access := range authorizer.accesses {
		if access != wantAccess {
			t.Fatalf("authorizer access %d = %#v, want %#v", index, access, wantAccess)
		}
	}
}

func TestUCIContextResolverKeepsClientBindingsIsolated(t *testing.T) {
	refA := contextResolverTestRefA()
	refB := contextResolverTestRefB()
	catalog := &contextResolverCatalogFake{}
	authorizer := &contextResolverAuthorizerFake{}
	resolver := NewContextResolver(catalog, authorizer)

	for _, binding := range []struct {
		client    string
		principal string
		ref       ContextRef
	}{
		{client: contextResolverTestClientA, principal: contextResolverTestPrincipal + "-a", ref: refA},
		{client: contextResolverTestClientB, principal: contextResolverTestPrincipal + "-b", ref: refB},
	} {
		ref := binding.ref
		resolved, err := resolver.Resolve(context.Background(), contextResolverTestInput(binding.client, binding.principal, &ref, nil))
		if err != nil {
			t.Fatalf("bind %s: %v", binding.client, err)
		}
		assertContextResolverRef(t, resolved.Ref(), binding.ref)
	}

	for _, reuse := range []struct {
		client    string
		principal string
		want      ContextRef
	}{
		{client: contextResolverTestClientA, principal: contextResolverTestPrincipal + "-a", want: refA},
		{client: contextResolverTestClientB, principal: contextResolverTestPrincipal + "-b", want: refB},
	} {
		resolved, err := resolver.Resolve(context.Background(), contextResolverTestInput(reuse.client, reuse.principal, nil, nil))
		if err != nil {
			t.Fatalf("reuse %s: %v", reuse.client, err)
		}
		assertContextResolverRef(t, resolved.Ref(), reuse.want)
	}

	assertContextResolverCatalogCalls(t, catalog, refA, refB, refA, refB)
	if authorizer.calls != 4 {
		t.Fatalf("authorizer calls = %d, want 4", authorizer.calls)
	}
}

func TestUCIContextResolverReauthorizesEveryReuse(t *testing.T) {
	ref := contextResolverTestRefA()
	catalog := &contextResolverCatalogFake{}
	authorizer := &contextResolverAuthorizerFake{failures: []error{
		nil,
		errors.New("revoked principal uci-test-principal for C:\\private\\worktree"),
	}}
	resolver := NewContextResolver(catalog, authorizer)

	bound, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil))
	if err != nil {
		t.Fatalf("initial Resolve() error = %v", err)
	}
	assertContextResolverRef(t, bound.Ref(), ref)

	_, err = resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, nil))
	assertContextResolverCode(t, err, "PERMISSION_DENIED")
	assertContextResolverCatalogCalls(t, catalog, ref, ref)
	if authorizer.calls != 2 {
		t.Fatalf("authorizer calls = %d, want 2", authorizer.calls)
	}
	_, err = resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, nil))
	assertContextResolverCode(t, err, "CONTEXT_REQUIRED")
	assertContextResolverCatalogCalls(t, catalog, ref, ref)
	if authorizer.calls != 2 {
		t.Fatalf("authorizer calls after revoked binding = %d, want 2", authorizer.calls)
	}
}

func TestUCIContextResolverDeniesRevokedAndPrivateContexts(t *testing.T) {
	revoked := contextResolverTestRefA()
	private := contextResolverTestRefB()

	for _, test := range []struct {
		name string
		ref  ContextRef
		err  error
	}{
		{
			name: "revoked context",
			ref:  revoked,
			err:  errors.New("revoked source 20000000-0000-4000-8000-000000000001 at C:\\private\\revoked"),
		},
		{
			name: "private checkout",
			ref:  private,
			err:  errors.New("private checkout 30000000-0000-4000-8000-000000000002 at C:\\private\\checkout"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &contextResolverCatalogFake{}
			authorizer := &contextResolverAuthorizerFake{failures: []error{test.err}}
			resolver := NewContextResolver(catalog, authorizer)
			ref := test.ref

			_, err := resolver.Resolve(context.Background(), contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil))
			assertContextResolverCode(t, err, "PERMISSION_DENIED")
			assertContextResolverCatalogCalls(t, catalog, test.ref)
			if authorizer.calls != 1 {
				t.Fatalf("authorizer calls = %d, want 1", authorizer.calls)
			}
		})
	}
}

func TestUCIContextResolverErrorsAreNonDisclosing(t *testing.T) {
	canonical := contextResolverTestRefA()
	mismatched := canonical
	mismatched.SourceID = contextResolverTestSourceB

	for _, test := range []struct {
		name            string
		input           ResolveContextInput
		catalogFailures []contextResolverCatalogFailure
		authorizerError error
		wantCode        string
		forbidden       []string
	}{
		{
			name:     "missing context",
			input:    contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, nil),
			wantCode: "CONTEXT_REQUIRED",
			forbidden: []string{
				contextResolverTestSourceA,
				contextResolverTestCheckoutA,
				contextResolverTestViewA,
				"candidate-count",
			},
		},
		{
			name: "relationship mismatch",
			input: func() ResolveContextInput {
				ref := mismatched
				return contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil)
			}(),
			catalogFailures: []contextResolverCatalogFailure{{
				ref: mismatched,
				err: errors.New("source 20000000-0000-4000-8000-000000000002 conflicts with checkout 30000000-0000-4000-8000-000000000001 at C:\\private\\relationship"),
			}},
			wantCode: "CONTEXT_MISMATCH",
			forbidden: []string{
				contextResolverTestSourceB,
				contextResolverTestCheckoutA,
				contextResolverTestViewA,
				"C:\\private\\relationship",
			},
		},
		{
			name: "authorization denial",
			input: func() ResolveContextInput {
				ref := canonical
				return contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil)
			}(),
			authorizerError: errors.New("principal uci-test-principal denied private checkout 30000000-0000-4000-8000-000000000001 at C:\\private\\denied"),
			wantCode:        "PERMISSION_DENIED",
			forbidden: []string{
				contextResolverTestPrincipal,
				contextResolverTestCheckoutA,
				"C:\\private\\denied",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &contextResolverCatalogFake{failures: test.catalogFailures}
			authorizer := &contextResolverAuthorizerFake{failures: []error{test.authorizerError}}
			resolver := NewContextResolver(catalog, authorizer)

			_, err := resolver.Resolve(context.Background(), test.input)
			assertContextResolverCode(t, err, test.wantCode)
			for _, value := range test.forbidden {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("non-disclosing error %q leaked %q", err, value)
				}
			}
		})
	}
}

type contextResolverCatalogFailure struct {
	ref ContextRef
	err error
}

type contextResolverCatalogRecord struct {
	lookup ContextRef
	record ContextRecord
}

type contextResolverCatalogFake struct {
	failures []contextResolverCatalogFailure
	records  []contextResolverCatalogRecord
	calls    []ContextRef
}

func (catalog *contextResolverCatalogFake) LoadContext(_ context.Context, ref ContextRef) (ContextRecord, error) {
	catalog.calls = append(catalog.calls, ref)
	for _, failure := range catalog.failures {
		if contextResolverRefsEqual(ref, failure.ref) {
			return ContextRecord{}, failure.err
		}
	}
	for _, stored := range catalog.records {
		if contextResolverRefsEqual(ref, stored.lookup) {
			return stored.record, nil
		}
	}
	return ContextRecord{Ref: ref, AuthRealm: contextResolverTestRealm}, nil
}

type contextResolverAuthorizerFake struct {
	failures []error
	calls    int
	accesses []ContextAccess
}

func (authorizer *contextResolverAuthorizerFake) AuthorizeContext(_ context.Context, access ContextAccess) error {
	call := authorizer.calls
	authorizer.calls++
	authorizer.accesses = append(authorizer.accesses, access)
	if call < len(authorizer.failures) {
		return authorizer.failures[call]
	}
	return nil
}

func contextResolverTestInput(clientSessionID, principal string, ref *ContextRef, candidates []ContextRef) ResolveContextInput {
	return ResolveContextInput{
		ClientSessionID: clientSessionID,
		AuthRealm:       contextResolverTestRealm,
		Principal:       principal,
		Ref:             ref,
		Candidates:      candidates,
	}
}

func contextResolverTestAccess(principal string, ref ContextRef) ContextAccess {
	return ContextAccess{
		AuthRealm:  contextResolverTestRealm,
		Principal:  principal,
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
	}
}

func contextResolverTestRefA() ContextRef {
	return contextResolverTestRef(contextResolverTestSourceA, contextResolverTestCheckoutA, contextResolverTestViewA, 7)
}

func contextResolverTestRefB() ContextRef {
	return contextResolverTestRef(contextResolverTestSourceB, contextResolverTestCheckoutB, contextResolverTestViewB, 11)
}

func contextResolverTestRef(sourceID, checkoutID, viewID string, generation int64) ContextRef {
	spaceID := contextResolverTestSpaceA
	return ContextRef{
		SpaceID:           &spaceID,
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: contextResolverTestProfile,
		Generation:        generation,
	}
}

func assertContextResolverCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Resolve() error = nil, want %s", want)
	}
	if got := err.Error(); got != want {
		t.Fatalf("Resolve() error = %q, want closed code %q", got, want)
	}
}

func assertContextResolverRef(t *testing.T, got, want ContextRef) {
	t.Helper()
	if !contextResolverRefsEqual(got, want) {
		t.Fatalf("resolved ref = %#v, want %#v", got, want)
	}
}

func assertContextResolverCatalogCalls(t *testing.T, catalog *contextResolverCatalogFake, want ...ContextRef) {
	t.Helper()
	if len(catalog.calls) != len(want) {
		t.Fatalf("catalog calls = %#v, want %d calls", catalog.calls, len(want))
	}
	for index := range want {
		if !contextResolverRefsEqual(catalog.calls[index], want[index]) {
			t.Fatalf("catalog call %d = %#v, want %#v", index, catalog.calls[index], want[index])
		}
	}
}

func contextResolverRefsEqual(left, right ContextRef) bool {
	return contextResolverOptionalStringsEqual(left.SpaceID, right.SpaceID) &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func contextResolverOptionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
