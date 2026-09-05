package codeintel_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/handlers/codeintel"
)

const uciWatcherCheckoutID = "checkout:opaque:watcher"

func TestUCIWatcherDebouncesAndCoalescesRepeatedPaths(t *testing.T) {
	fixture := newUCIWatcherFixture(t, nil, 16, "")
	startUCIWatcher(t, fixture, context.Background())

	firstPath := filepath.Join(fixture.root, "internal", "first.go")
	secondPath := filepath.Join(fixture.root, "internal", "second.go")
	fixture.source.events <- fsnotify.Event{Name: firstPath, Op: fsnotify.Write}
	_ = awaitUCIWatcherTimers(t, fixture.clock, 2)

	fixture.source.events <- fsnotify.Event{Name: firstPath, Op: fsnotify.Write}
	_ = awaitUCIWatcherTimers(t, fixture.clock, 1)
	fixture.source.events <- fsnotify.Event{Name: secondPath, Op: fsnotify.Create}
	latestDebounce := awaitUCIWatcherTimers(t, fixture.clock, 1)[0]

	require.Empty(t, fixture.registry.dirtySnapshot(), "hints must wait for the injected debounce timer")
	latestDebounce.fire(t)

	changes := awaitUCIWatcherDirtyChanges(t, fixture.registry, 2)
	requireUCIWatcherDirtyPaths(t, changes, "internal/first.go", "internal/second.go")
	stopUCIWatcher(t, fixture)
}

func TestUCIWatcherFlushesAtMaximumBatchDelay(t *testing.T) {
	fixture := newUCIWatcherFixture(t, nil, 16, "")
	startUCIWatcher(t, fixture, context.Background())

	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "first.go"), Op: fsnotify.Write}
	initialTimers := awaitUCIWatcherTimers(t, fixture.clock, 2)
	maximumDelay := uciWatcherTimerWithDelay(t, initialTimers, fixture.maximumDelay)

	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "second.go"), Op: fsnotify.Write}
	_ = awaitUCIWatcherTimers(t, fixture.clock, 1)

	require.Empty(t, fixture.registry.dirtySnapshot(), "the trailing debounce remains pending")
	maximumDelay.fire(t)

	changes := awaitUCIWatcherDirtyChanges(t, fixture.registry, 2)
	requireUCIWatcherDirtyPaths(t, changes, "first.go", "second.go")
	stopUCIWatcher(t, fixture)
}

func TestUCIWatcherSequencesEventArrivingDuringPersistence(t *testing.T) {
	fixture := newUCIWatcherFixture(t, nil, 16, "")
	fixture.registry.blockFirstRecord = make(chan struct{})
	startUCIWatcher(t, fixture, context.Background())

	firstPath := filepath.Join(fixture.root, "first.go")
	fixture.source.events <- fsnotify.Event{Name: firstPath, Op: fsnotify.Write}
	initialTimers := awaitUCIWatcherTimers(t, fixture.clock, 2)
	uciWatcherTimerWithDelay(t, initialTimers, fixture.debounceDelay).fire(t)

	firstStarted := awaitUCIWatcherFirstRecord(t, fixture.registry)
	require.Equal(t, "first.go", firstStarted.RelativePath)
	require.Equal(t, int64(1), firstStarted.Sequence)

	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "later.go"), Op: fsnotify.Write}
	close(fixture.registry.blockFirstRecord)

	firstPersisted := awaitUCIWatcherDirtyChanges(t, fixture.registry, 1)[0]
	require.Equal(t, firstStarted, firstPersisted)

	nextTimers := awaitUCIWatcherTimers(t, fixture.clock, 2)
	uciWatcherTimerWithDelay(t, nextTimers, fixture.debounceDelay).fire(t)
	secondPersisted := awaitUCIWatcherDirtyChanges(t, fixture.registry, 1)[0]
	require.Equal(t, "later.go", secondPersisted.RelativePath)
	require.Equal(t, int64(2), secondPersisted.Sequence, "an event observed while the first persistence is in flight belongs to the next sequence")
	stopUCIWatcher(t, fixture)
}

func TestUCIWatcherPersistsRenamePairs(t *testing.T) {
	fixture := newUCIWatcherFixture(t, nil, 16, "")
	startUCIWatcher(t, fixture, context.Background())

	oldPath := filepath.Join(fixture.root, "internal", "old.go")
	newPath := filepath.Join(fixture.root, "internal", "new.go")
	fixture.source.events <- fsnotify.Event{Name: oldPath, Op: fsnotify.Rename}
	_ = awaitUCIWatcherTimers(t, fixture.clock, 2)
	fixture.source.events <- fsnotify.Event{Name: newPath, Op: fsnotify.Create}
	latestDebounce := awaitUCIWatcherTimers(t, fixture.clock, 1)[0]
	latestDebounce.fire(t)

	changes := awaitUCIWatcherDirtyChanges(t, fixture.registry, 2)
	requireUCIWatcherDirtyPaths(t, changes, "internal/old.go", "internal/new.go")
	stopUCIWatcher(t, fixture)
}

func TestUCIWatcherMarksRescanForLossyWatchSignals(t *testing.T) {
	t.Run("watch registration failure", func(t *testing.T) {
		fixture := newUCIWatcherFixture(t, nil, 16, "")
		registrationErr := errors.New("watch registration failed")
		fixture.source.addErr = registrationErr

		err := fixture.watcher.Start(context.Background())
		require.ErrorIs(t, err, registrationErr)
		awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanRestart)
		awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanWatcherRegistrationFailure)
		requireUCIWatcherDoneError(t, fixture.watcher, registrationErr)
		require.Empty(t, fixture.registry.dirtySnapshot())
	})

	t.Run("fsnotify overflow", func(t *testing.T) {
		fixture := newUCIWatcherFixture(t, nil, 16, "")
		startUCIWatcher(t, fixture, context.Background())
		fixture.source.errors <- fsnotify.ErrEventOverflow
		awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanOverflow)
		stopUCIWatcher(t, fixture)
		require.Empty(t, fixture.registry.dirtySnapshot())
	})

	t.Run("root move", func(t *testing.T) {
		fixture := newUCIWatcherFixture(t, nil, 16, "")
		startUCIWatcher(t, fixture, context.Background())
		fixture.source.events <- fsnotify.Event{Name: fixture.root, Op: fsnotify.Rename}
		awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanMove)
		stopUCIWatcher(t, fixture)
		require.Empty(t, fixture.registry.dirtySnapshot())
	})

	gitTransitions := []struct {
		name string
		path string
	}{
		{name: "HEAD", path: "HEAD"},
		{name: "index", path: "index"},
		{name: "branch ref", path: filepath.Join("refs", "heads", "main")},
	}
	for _, transition := range gitTransitions {
		transition := transition
		t.Run("git "+transition.name+" transition", func(t *testing.T) {
			fixture := newUCIWatcherFixture(t, nil, 16, "")
			startUCIWatcher(t, fixture, context.Background())
			fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.gitDir, transition.path), Op: fsnotify.Write}
			awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanGitTransition)
			stopUCIWatcher(t, fixture)
			require.Empty(t, fixture.registry.dirtySnapshot(), "Git metadata is a rescan trigger, not a source-body hint")
		})
	}
}

func TestUCIWatcherBoundsPendingHintsWithoutDeliveryClaims(t *testing.T) {
	fixture := newUCIWatcherFixture(t, nil, 1, "")
	startUCIWatcher(t, fixture, context.Background())

	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "first.go"), Op: fsnotify.Write}
	_ = awaitUCIWatcherTimers(t, fixture.clock, 2)
	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "second.go"), Op: fsnotify.Write}
	awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanOverflow)

	stopUCIWatcher(t, fixture)
	assertUCIWatcherHintOnlySurface(t)
}

func TestUCIWatcherCancelsAndRestartsSafely(t *testing.T) {
	registry := newFakeUCIWatcherRegistry()
	first := newUCIWatcherFixture(t, registry, 16, "")
	ctx, cancel := context.WithCancel(context.Background())
	startUCIWatcher(t, first, ctx)

	first.source.events <- fsnotify.Event{Name: filepath.Join(first.root, "pending.go"), Op: fsnotify.Write}
	_ = awaitUCIWatcherTimers(t, first.clock, 2)
	cancel()

	awaitUCIWatcherSourceClosed(t, first.source)
	requireUCIWatcherDoneClosed(t, first.watcher)
	require.Empty(t, registry.dirtySnapshot(), "a cancelled watcher must not persist a timer that was never fired")

	successor := newUCIWatcherFixture(t, registry, 16, first.root)
	startUCIWatcher(t, successor, context.Background())
	stopUCIWatcher(t, successor)
}

func TestUCIWatcherContinuesPersistedSequenceAfterRestart(t *testing.T) {
	registry := newFakeUCIWatcherRegistry()
	registry.dirtySequence = 41
	fixture := newUCIWatcherFixture(t, registry, 16, "")
	startUCIWatcher(t, fixture, context.Background())

	fixture.source.events <- fsnotify.Event{Name: filepath.Join(fixture.root, "continued.go"), Op: fsnotify.Write}
	timers := awaitUCIWatcherTimers(t, fixture.clock, 2)
	uciWatcherTimerWithDelay(t, timers, fixture.debounceDelay).fire(t)

	change := awaitUCIWatcherDirtyChanges(t, registry, 1)[0]
	require.Equal(t, int64(42), change.Sequence, "a successor watcher must continue after the durable dirty high-water mark")
	stopUCIWatcher(t, fixture)
}

type uciWatcherFixture struct {
	root          string
	gitDir        string
	debounceDelay time.Duration
	maximumDelay  time.Duration
	watcher       *codeintel.UCIWatcher
	registry      *fakeUCIWatcherRegistry
	source        *fakeUCIWatcherSource
	clock         *fakeUCIWatcherClock
}

func newUCIWatcherFixture(t *testing.T, registry *fakeUCIWatcherRegistry, capacity int, root string) *uciWatcherFixture {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	if registry == nil {
		registry = newFakeUCIWatcherRegistry()
	}

	fixture := &uciWatcherFixture{
		root:          root,
		gitDir:        filepath.Join(root, ".git"),
		debounceDelay: 10 * time.Millisecond,
		maximumDelay:  50 * time.Millisecond,
		registry:      registry,
		source:        newFakeUCIWatcherSource(),
		clock:         newFakeUCIWatcherClock(),
	}
	var err error
	fixture.watcher, err = codeintel.NewUCIWatcher(codeintel.UCIWatcherConfig{
		Registry:      fixture.registry,
		Source:        fixture.source,
		Clock:         fixture.clock,
		CheckoutID:    uciWatcherCheckoutID,
		RootPath:      fixture.root,
		GitDir:        fixture.gitDir,
		DebounceDelay: fixture.debounceDelay,
		MaxBatchDelay: fixture.maximumDelay,
		QueueCapacity: capacity,
	})
	require.NoError(t, err)
	return fixture
}

func startUCIWatcher(t *testing.T, fixture *uciWatcherFixture, ctx context.Context) {
	t.Helper()
	require.NoError(t, fixture.watcher.Start(ctx))
	awaitUCIWatcherRescan(t, fixture.registry, codeintel.UCILocalRescanRestart)
	require.Equal(t, []string{fixture.root}, fixture.source.addedPaths())
}

func stopUCIWatcher(t *testing.T, fixture *uciWatcherFixture) {
	t.Helper()
	require.NoError(t, fixture.watcher.Stop())
	awaitUCIWatcherSourceClosed(t, fixture.source)
	requireUCIWatcherDoneClosed(t, fixture.watcher)
}

type fakeUCIWatcherSource struct {
	mu       sync.Mutex
	addErr   error
	addPaths []string

	events chan fsnotify.Event
	errors chan error
	closed chan struct{}

	closeOnce sync.Once
}

func newFakeUCIWatcherSource() *fakeUCIWatcherSource {
	return &fakeUCIWatcherSource{
		events: make(chan fsnotify.Event, 32),
		errors: make(chan error, 32),
		closed: make(chan struct{}),
	}
}

func (source *fakeUCIWatcherSource) Add(root string) error {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.addPaths = append(source.addPaths, root)
	return source.addErr
}

func (source *fakeUCIWatcherSource) Events() <-chan fsnotify.Event {
	return source.events
}

func (source *fakeUCIWatcherSource) Errors() <-chan error {
	return source.errors
}

func (source *fakeUCIWatcherSource) Close() error {
	source.closeOnce.Do(func() { close(source.closed) })
	return nil
}

func (source *fakeUCIWatcherSource) addedPaths() []string {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]string(nil), source.addPaths...)
}

type fakeUCIWatcherClock struct {
	created chan *fakeUCIWatcherTimer
}

func newFakeUCIWatcherClock() *fakeUCIWatcherClock {
	return &fakeUCIWatcherClock{created: make(chan *fakeUCIWatcherTimer, 32)}
}

func (clock *fakeUCIWatcherClock) Now() time.Time {
	return time.Unix(0, 0).UTC()
}

func (clock *fakeUCIWatcherClock) NewTimer(delay time.Duration) codeintel.UCIWatcherTimer {
	timer := &fakeUCIWatcherTimer{
		delay: delay,
		fired: make(chan time.Time, 1),
	}
	clock.created <- timer
	return timer
}

type fakeUCIWatcherTimer struct {
	mu      sync.Mutex
	delay   time.Duration
	fired   chan time.Time
	stopped bool
}

func (timer *fakeUCIWatcherTimer) C() <-chan time.Time {
	return timer.fired
}

func (timer *fakeUCIWatcherTimer) Stop() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	if timer.stopped {
		return false
	}
	timer.stopped = true
	return true
}

func (timer *fakeUCIWatcherTimer) fire(t *testing.T) {
	t.Helper()
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	require.False(t, stopped, "the selected injected timer must still be active")
	select {
	case timer.fired <- time.Unix(0, 0).UTC():
	default:
		t.Fatal("injected timer fired more than once")
	}
}

type fakeUCIWatcherRegistry struct {
	mu sync.Mutex

	dirty   []codeintel.UCILocalDirtyChange
	rescans []codeintel.UCILocalRescanCause

	dirtyObserved  chan codeintel.UCILocalDirtyChange
	rescanObserved chan codeintel.UCILocalRescanCause
	firstEntered   chan codeintel.UCILocalDirtyChange

	blockFirstRecord chan struct{}
	recordCount      int
	dirtySequence    int64
}

func newFakeUCIWatcherRegistry() *fakeUCIWatcherRegistry {
	return &fakeUCIWatcherRegistry{
		dirtyObserved:  make(chan codeintel.UCILocalDirtyChange, 32),
		rescanObserved: make(chan codeintel.UCILocalRescanCause, 32),
		firstEntered:   make(chan codeintel.UCILocalDirtyChange, 1),
	}
}

func (registry *fakeUCIWatcherRegistry) RecordDirty(ctx context.Context, change codeintel.UCILocalDirtyChange) (codeintel.UCILocalCheckoutRecord, error) {
	registry.mu.Lock()
	registry.recordCount++
	block := registry.recordCount == 1 && registry.blockFirstRecord != nil
	blockFirstRecord := registry.blockFirstRecord
	registry.mu.Unlock()

	if block {
		registry.firstEntered <- change
		select {
		case <-blockFirstRecord:
		case <-ctx.Done():
			return codeintel.UCILocalCheckoutRecord{}, ctx.Err()
		}
	}

	registry.mu.Lock()
	registry.dirty = append(registry.dirty, change)
	if change.Sequence > registry.dirtySequence {
		registry.dirtySequence = change.Sequence
	}
	dirtySequence := registry.dirtySequence
	registry.mu.Unlock()
	registry.dirtyObserved <- change
	return codeintel.UCILocalCheckoutRecord{CheckoutID: change.CheckoutID, DirtySequence: dirtySequence}, nil
}

func (registry *fakeUCIWatcherRegistry) RequireRescan(_ context.Context, checkoutID string, cause codeintel.UCILocalRescanCause) (codeintel.UCILocalCheckoutRecord, error) {
	registry.mu.Lock()
	registry.rescans = append(registry.rescans, cause)
	dirtySequence := registry.dirtySequence
	registry.mu.Unlock()
	registry.rescanObserved <- cause
	return codeintel.UCILocalCheckoutRecord{CheckoutID: checkoutID, DirtySequence: dirtySequence}, nil
}

func (registry *fakeUCIWatcherRegistry) dirtySnapshot() []codeintel.UCILocalDirtyChange {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return append([]codeintel.UCILocalDirtyChange(nil), registry.dirty...)
}

func awaitUCIWatcherTimers(t *testing.T, clock *fakeUCIWatcherClock, count int) []*fakeUCIWatcherTimer {
	t.Helper()
	timers := make([]*fakeUCIWatcherTimer, 0, count)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(timers) < count {
		select {
		case timer := <-clock.created:
			timers = append(timers, timer)
		case <-deadline.C:
			t.Fatalf("watcher did not create %d injected timer(s); received %d", count, len(timers))
		}
	}
	return timers
}

func uciWatcherTimerWithDelay(t *testing.T, timers []*fakeUCIWatcherTimer, delay time.Duration) *fakeUCIWatcherTimer {
	t.Helper()
	for _, timer := range timers {
		if timer.delay == delay {
			return timer
		}
	}
	t.Fatalf("missing timer with delay %s", delay)
	return nil
}

func awaitUCIWatcherDirtyChanges(t *testing.T, registry *fakeUCIWatcherRegistry, count int) []codeintel.UCILocalDirtyChange {
	t.Helper()
	changes := make([]codeintel.UCILocalDirtyChange, 0, count)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(changes) < count {
		select {
		case change := <-registry.dirtyObserved:
			changes = append(changes, change)
		case <-deadline.C:
			t.Fatalf("watcher persisted %d dirty change(s); want %d", len(changes), count)
		}
	}
	return changes
}

func awaitUCIWatcherFirstRecord(t *testing.T, registry *fakeUCIWatcherRegistry) codeintel.UCILocalDirtyChange {
	t.Helper()
	select {
	case change := <-registry.firstEntered:
		return change
	case <-time.After(time.Second):
		t.Fatal("watcher did not begin the first dirty persistence")
		return codeintel.UCILocalDirtyChange{}
	}
}

func awaitUCIWatcherRescan(t *testing.T, registry *fakeUCIWatcherRegistry, expected codeintel.UCILocalRescanCause) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case cause := <-registry.rescanObserved:
			if cause == expected {
				return
			}
		case <-deadline.C:
			t.Fatalf("watcher did not require rescan cause %q", expected)
		}
	}
}

func awaitUCIWatcherSourceClosed(t *testing.T, source *fakeUCIWatcherSource) {
	t.Helper()
	select {
	case <-source.closed:
	case <-time.After(time.Second):
		t.Fatal("watcher did not close its event source")
	}
}

func requireUCIWatcherDoneClosed(t *testing.T, watcher *codeintel.UCIWatcher) {
	t.Helper()
	select {
	case err, open := <-watcher.Done():
		require.False(t, open, "clean cancellation or Stop must close Done without a terminal error, got %v", err)
	case <-time.After(time.Second):
		t.Fatal("watcher did not finish after cancellation or Stop")
	}
}

func requireUCIWatcherDoneError(t *testing.T, watcher *codeintel.UCIWatcher, expected error) {
	t.Helper()
	select {
	case err, open := <-watcher.Done():
		require.True(t, open, "abnormal watcher termination must report its terminal error")
		require.ErrorIs(t, err, expected)
	case <-time.After(time.Second):
		t.Fatal("watcher did not report its terminal error")
	}

	_, open := <-watcher.Done()
	require.False(t, open, "Done must close after its single terminal error")
}

func requireUCIWatcherDirtyPaths(t *testing.T, changes []codeintel.UCILocalDirtyChange, expectedPaths ...string) {
	t.Helper()
	actualPaths := make(map[string]int64, len(changes))
	sequences := make([]int64, 0, len(changes))
	for _, change := range changes {
		require.Equal(t, uciWatcherCheckoutID, change.CheckoutID)
		_, duplicate := actualPaths[change.RelativePath]
		require.Falsef(t, duplicate, "dirty path %q must be coalesced", change.RelativePath)
		actualPaths[change.RelativePath] = change.Sequence
		sequences = append(sequences, change.Sequence)
	}

	require.Len(t, actualPaths, len(expectedPaths))
	for _, expectedPath := range expectedPaths {
		_, found := actualPaths[expectedPath]
		require.Truef(t, found, "missing dirty path %q in %v", expectedPath, actualPaths)
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	for index, sequence := range sequences {
		require.Equalf(t, int64(index+1), sequence, "dirty sequences must be a local monotonic batch sequence")
	}
}

func assertUCIWatcherHintOnlySurface(t *testing.T) {
	t.Helper()
	registryPort := reflect.TypeOf((*codeintel.UCILocalRegistryPort)(nil)).Elem()
	require.Equal(t, reflect.Interface, registryPort.Kind())
	require.Equal(t, 2, registryPort.NumMethod(), "watcher registry access must remain a dirty/rescan-only port")
	for _, method := range []string{"RecordDirty", "RequireRescan"} {
		_, found := registryPort.MethodByName(method)
		require.Truef(t, found, "watcher registry port must expose %s", method)
	}

	watcherType := reflect.TypeOf((*codeintel.UCIWatcher)(nil))
	configType := reflect.TypeOf(codeintel.UCIWatcherConfig{})
	for _, forbidden := range []string{
		"Authorize", "ReadSource", "Scan", "Index", "Publish", "Query", "Graph",
		"ExactlyOnce", "NoEventLoss", "NoEventLossGuarantee", "EventCompleteness",
	} {
		_, methodFound := watcherType.MethodByName(forbidden)
		require.Falsef(t, methodFound, "watcher must not claim or perform %s", forbidden)
		_, fieldFound := configType.FieldByName(forbidden)
		require.Falsef(t, fieldFound, "watcher configuration must not claim or perform %s", forbidden)
	}
}
