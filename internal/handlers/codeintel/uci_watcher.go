package codeintel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	uciWatcherRescanQueueCapacity = 4
	uciWatcherChangeQueueCapacity = 1
)

var errUCIWatcherAlreadyStarted = errors.New("uci watcher: already started")

// UCILocalRegistryPort is the watcher-facing operational-state boundary. It
// intentionally cannot authorize, scan, reconcile, or publish a View.
type UCILocalRegistryPort interface {
	RecordDirty(context.Context, UCILocalDirtyChange) (UCILocalCheckoutRecord, error)
	RequireRescan(context.Context, string, UCILocalRescanCause) (UCILocalCheckoutRecord, error)
}

// UCIWatcherEventSource supplies native filesystem hints. A closed source is
// not proof that no changes happened; the watcher records a rescan instead.
type UCIWatcherEventSource interface {
	Add(string) error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

// uciWatcherEventAdmitter is an optional production-source filter. The generic
// watcher remains registry-only; the adapter decides which filesystem paths it
// is safe to observe.
type uciWatcherEventAdmitter interface {
	AcceptsEvent(fsnotify.Event) bool
}

// UCIWatcherTimer is the timer boundary used by the deterministic debounce
// state machine.
type UCIWatcherTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// UCIWatcherClock constructs timers for the deterministic debounce state
// machine.
type UCIWatcherClock interface {
	NewTimer(time.Duration) UCIWatcherTimer
}

// UCIWatcherConfig binds one watcher to one already-authorized checkout's
// local operational registry. Filesystem events remain hints only.
type UCIWatcherConfig struct {
	Registry      UCILocalRegistryPort
	Source        UCIWatcherEventSource
	Clock         UCIWatcherClock
	CheckoutID    string
	RootPath      string
	GitDir        string
	DebounceDelay time.Duration
	MaxBatchDelay time.Duration
	QueueCapacity int
}

// UCIWatcher coalesces filesystem hints into bounded dirty-state writes. It
// does not read source bodies or make any statement about event completeness.
type UCIWatcher struct {
	config   UCIWatcherConfig
	rootPath string
	gitDir   string

	changes chan struct{}

	done chan error

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc

	closeSourceOnce sync.Once
	closeSourceErr  error
	finishOnce      sync.Once
	wg              sync.WaitGroup
}

// NewUCIWatcher constructs an unstarted watcher. The default clock uses real
// timers; tests can provide a deterministic clock through the config.
func NewUCIWatcher(config UCIWatcherConfig) (*UCIWatcher, error) {
	if config.Registry == nil {
		return nil, errors.New("uci watcher: registry is required")
	}
	if config.Source == nil {
		return nil, errors.New("uci watcher: event source is required")
	}
	if config.CheckoutID == "" {
		return nil, errors.New("uci watcher: checkout ID is required")
	}
	if config.RootPath == "" {
		return nil, errors.New("uci watcher: root path is required")
	}
	if config.GitDir == "" {
		return nil, errors.New("uci watcher: Git directory is required")
	}
	if config.DebounceDelay <= 0 {
		return nil, errors.New("uci watcher: debounce delay must be positive")
	}
	if config.MaxBatchDelay <= 0 {
		return nil, errors.New("uci watcher: max batch delay must be positive")
	}
	if config.QueueCapacity <= 0 {
		return nil, errors.New("uci watcher: queue capacity must be positive")
	}
	if config.Clock == nil {
		config.Clock = uciWatcherWallClock{}
	}

	return &UCIWatcher{
		config:   config,
		rootPath: filepath.Clean(config.RootPath),
		gitDir:   filepath.Clean(config.GitDir),
		changes:  make(chan struct{}, uciWatcherChangeQueueCapacity),
		done:     make(chan error, 1),
	}, nil
}

// Start records conservative restart recovery, registers the root event
// source, then begins consuming hints. Git metadata paths received through the
// source are classified as rescan triggers. A watcher is one-shot; construct a
// successor watcher with a fresh source after stopping it.
func (watcher *UCIWatcher) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("uci watcher: context is required")
	}
	if err := parent.Err(); err != nil {
		return err
	}

	watcher.mu.Lock()
	if watcher.started {
		watcher.mu.Unlock()
		return errUCIWatcherAlreadyStarted
	}
	ctx, cancel := context.WithCancel(parent)
	watcher.started = true
	watcher.cancel = cancel
	watcher.wg.Add(1)
	watcher.mu.Unlock()

	restartState, err := watcher.config.Registry.RequireRescan(ctx, watcher.config.CheckoutID, UCILocalRescanRestart)
	if err != nil {
		return watcher.failStart(fmt.Errorf("uci watcher: record restart recovery: %w", err))
	}
	if restartState.DirtySequence == int64(^uint64(0)>>1) {
		return watcher.failStart(errors.New("uci watcher: dirty sequence exhausted"))
	}
	nextSequence := restartState.DirtySequence + 1
	if err := watcher.addWatches(); err != nil {
		registrationErr := fmt.Errorf("uci watcher: register event source: %w", err)
		if _, rescanErr := watcher.config.Registry.RequireRescan(context.Background(), watcher.config.CheckoutID, UCILocalRescanWatcherRegistrationFailure); rescanErr != nil {
			registrationErr = fmt.Errorf("%v; record registration failure: %w", registrationErr, rescanErr)
		} else {
			watcher.signalChange()
		}
		return watcher.failStart(registrationErr)
	}

	go func() {
		defer watcher.wg.Done()
		watcher.finish(watcher.run(ctx, nextSequence))
	}()
	return nil
}

// Stop cancels the watcher, closes its event source once, and waits for its
// state machine to stop. It never clears local dirty or rescan state.
func (watcher *UCIWatcher) Stop() error {
	watcher.mu.Lock()
	if !watcher.started {
		watcher.mu.Unlock()
		return nil
	}
	cancel := watcher.cancel
	watcher.mu.Unlock()

	cancel()
	closeErr := watcher.closeSource()
	watcher.wg.Wait()
	return closeErr
}

// Done closes when the watcher has stopped. It yields one error before close
// only when persistence or setup could not complete.
func (watcher *UCIWatcher) Done() <-chan error {
	return watcher.done
}

// Changes returns a bounded, coalesced notification for durable local state
// changes. It never signals startup recovery: each notification is emitted
// only after a dirty batch or non-startup rescan cause has been persisted.
func (watcher *UCIWatcher) Changes() <-chan struct{} {
	return watcher.changes
}

func (watcher *UCIWatcher) failStart(err error) error {
	watcher.mu.Lock()
	cancel := watcher.cancel
	watcher.mu.Unlock()
	cancel()
	_ = watcher.closeSource()
	watcher.finish(err)
	watcher.wg.Done()
	return err
}

func (watcher *UCIWatcher) addWatches() error {
	if err := watcher.config.Source.Add(watcher.rootPath); err != nil {
		return fmt.Errorf("add %q: %w", watcher.rootPath, err)
	}
	if !uciWatcherPathWithin(watcher.rootPath, watcher.gitDir) {
		if err := watcher.config.Source.Add(watcher.gitDir); err != nil {
			return fmt.Errorf("add private Git directory %q: %w", watcher.gitDir, err)
		}
	}
	return nil
}

func (watcher *UCIWatcher) run(parent context.Context, nextSequence int64) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer watcher.closeSource()

	batches := make(chan uciWatcherBatch, watcher.config.QueueCapacity)
	rescans := make(chan UCILocalRescanCause, uciWatcherRescanQueueCapacity)
	workerDone := make(chan error, 1)
	go func() {
		err := watcher.persist(ctx, batches, rescans, nextSequence)
		if err != nil {
			cancel()
		}
		workerDone <- err
	}()

	watcher.collect(ctx, batches, rescans)
	close(batches)
	close(rescans)

	if err := <-workerDone; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (watcher *UCIWatcher) collect(ctx context.Context, batches chan<- uciWatcherBatch, rescans chan<- UCILocalRescanCause) {
	newUCIWatcherCollector(watcher, batches, rescans).collect(ctx)
}

type uciWatcherCollector struct {
	watcher       *UCIWatcher
	batches       chan<- uciWatcherBatch
	rescans       chan<- UCILocalRescanCause
	events        <-chan fsnotify.Event
	sourceErrors  <-chan error
	queuedRescans map[UCILocalRescanCause]struct{}
	paths         []string
	pathSet       map[string]struct{}
	debounceTimer UCIWatcherTimer
	maxTimer      UCIWatcherTimer
	debounceC     <-chan time.Time
	maxC          <-chan time.Time
}

func newUCIWatcherCollector(watcher *UCIWatcher, batches chan<- uciWatcherBatch, rescans chan<- UCILocalRescanCause) *uciWatcherCollector {
	return &uciWatcherCollector{
		watcher:       watcher,
		batches:       batches,
		rescans:       rescans,
		events:        watcher.config.Source.Events(),
		sourceErrors:  watcher.config.Source.Errors(),
		queuedRescans: make(map[UCILocalRescanCause]struct{}, uciWatcherRescanQueueCapacity),
		pathSet:       make(map[string]struct{}),
	}
}

func (collector *uciWatcherCollector) collect(ctx context.Context) {
	defer collector.stopTimers()
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-collector.events:
			if !collector.handleEvent(ctx, event, open) {
				return
			}
		case _, open := <-collector.sourceErrors:
			if !collector.handleSourceError(ctx, open) {
				return
			}
		case <-collector.debounceC:
			collector.flush()
		case <-collector.maxC:
			collector.flush()
		}
	}
}

func (collector *uciWatcherCollector) queueRescan(cause UCILocalRescanCause) {
	if _, found := collector.queuedRescans[cause]; found {
		return
	}
	collector.queuedRescans[cause] = struct{}{}
	collector.rescans <- cause
}

func (collector *uciWatcherCollector) stopTimers() {
	if collector.debounceTimer != nil {
		collector.debounceTimer.Stop()
	}
	if collector.maxTimer != nil {
		collector.maxTimer.Stop()
	}
	collector.debounceTimer = nil
	collector.maxTimer = nil
	collector.debounceC = nil
	collector.maxC = nil
}

func (collector *uciWatcherCollector) flush() {
	if len(collector.paths) != 0 {
		batch := uciWatcherBatch{paths: append([]string(nil), collector.paths...)}
		select {
		case collector.batches <- batch:
		default:
			collector.queueRescan(UCILocalRescanOverflow)
		}
	}
	collector.paths = nil
	collector.pathSet = make(map[string]struct{})
	collector.stopTimers()
}

func (collector *uciWatcherCollector) handleEvent(ctx context.Context, event fsnotify.Event, open bool) bool {
	if ctx.Err() != nil {
		return false
	}
	if !open {
		collector.events = nil
		if collector.sourceErrors == nil {
			collector.flush()
			collector.queueRescan(UCILocalRescanOverflow)
			return false
		}
		return true
	}
	if admittingSource, ok := collector.watcher.config.Source.(uciWatcherEventAdmitter); ok && !admittingSource.AcceptsEvent(event) {
		return true
	}
	if event.Op&fsnotify.Create != 0 {
		if err := collector.watcher.config.Source.Add(event.Name); err != nil {
			collector.queueRescan(UCILocalRescanWatcherRegistrationFailure)
		}
	}
	if collector.watcher.eventIsGitTransition(event) {
		collector.queueRescan(UCILocalRescanGitTransition)
		return true
	}
	collector.queuePath(event.Name)
	if event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
		collector.queueRescan(UCILocalRescanMove)
	}
	return true
}

func (collector *uciWatcherCollector) queuePath(eventPath string) {
	relativePath, found := collector.watcher.relativeEventPath(eventPath)
	if !found {
		return
	}
	_, exists := collector.pathSet[relativePath]
	if !exists && len(collector.paths) >= collector.watcher.config.QueueCapacity {
		collector.queueRescan(UCILocalRescanOverflow)
		return
	}
	if !exists {
		collector.pathSet[relativePath] = struct{}{}
		collector.paths = append(collector.paths, relativePath)
	}
	if collector.debounceTimer != nil {
		collector.debounceTimer.Stop()
	}
	collector.debounceTimer = collector.watcher.config.Clock.NewTimer(collector.watcher.config.DebounceDelay)
	collector.debounceC = collector.debounceTimer.C()
	if collector.maxTimer == nil {
		collector.maxTimer = collector.watcher.config.Clock.NewTimer(collector.watcher.config.MaxBatchDelay)
		collector.maxC = collector.maxTimer.C()
	}
}

func (collector *uciWatcherCollector) handleSourceError(ctx context.Context, open bool) bool {
	if ctx.Err() != nil {
		return false
	}
	if open {
		collector.queueRescan(UCILocalRescanOverflow)
		return true
	}
	collector.sourceErrors = nil
	if collector.events != nil {
		return true
	}
	collector.flush()
	collector.queueRescan(UCILocalRescanOverflow)
	return false
}

func (watcher *UCIWatcher) persist(ctx context.Context, batches <-chan uciWatcherBatch, rescans <-chan UCILocalRescanCause, nextSequence int64) error {
	if nextSequence <= 0 {
		return errors.New("uci watcher: dirty sequence is invalid")
	}
	for batches != nil || rescans != nil {
		select {
		case <-ctx.Done():
			return nil
		case cause, open := <-rescans:
			if !open {
				rescans = nil
				continue
			}
			if err := watcher.persistRescan(ctx, cause); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		case batch, open := <-batches:
			if !open {
				batches = nil
				continue
			}
			if err := watcher.persistBatch(ctx, batch, &nextSequence); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
	return nil
}

func (watcher *UCIWatcher) persistRescan(ctx context.Context, cause UCILocalRescanCause) error {
	if _, err := watcher.config.Registry.RequireRescan(ctx, watcher.config.CheckoutID, cause); err != nil {
		return fmt.Errorf("uci watcher: persist rescan cause %q: %w", cause, err)
	}
	if cause != UCILocalRescanRestart {
		watcher.signalChange()
	}
	return nil
}

func (watcher *UCIWatcher) persistBatch(ctx context.Context, batch uciWatcherBatch, nextSequence *int64) error {
	for _, relativePath := range batch.paths {
		record, err := watcher.config.Registry.RecordDirty(ctx, UCILocalDirtyChange{
			CheckoutID:   watcher.config.CheckoutID,
			RelativePath: relativePath,
			Sequence:     *nextSequence,
		})
		if err != nil {
			return fmt.Errorf("uci watcher: persist dirty path %q: %w", relativePath, err)
		}
		if record.DirtySequence >= *nextSequence {
			*nextSequence = record.DirtySequence + 1
		} else {
			*nextSequence = *nextSequence + 1
		}
		if *nextSequence <= 0 {
			return errors.New("uci watcher: dirty sequence exhausted")
		}
	}
	watcher.signalChange()
	return nil
}

func (watcher *UCIWatcher) eventIsGitTransition(event fsnotify.Event) bool {
	return uciWatcherPathWithin(watcher.gitDir, event.Name)
}

func (watcher *UCIWatcher) relativeEventPath(eventPath string) (string, bool) {
	if eventPath == "" {
		return "", false
	}
	relativePath, err := filepath.Rel(watcher.rootPath, filepath.Clean(eventPath))
	if err != nil || relativePath == "." || !uciWatcherRelativePathIsSafe(relativePath) {
		return "", false
	}
	return filepath.ToSlash(relativePath), true
}

func (watcher *UCIWatcher) closeSource() error {
	watcher.closeSourceOnce.Do(func() {
		watcher.closeSourceErr = watcher.config.Source.Close()
	})
	if errors.Is(watcher.closeSourceErr, fsnotify.ErrClosed) {
		return nil
	}
	return watcher.closeSourceErr
}

func (watcher *UCIWatcher) signalChange() {
	select {
	case watcher.changes <- struct{}{}:
	default:
	}
}

func (watcher *UCIWatcher) finish(err error) {
	watcher.finishOnce.Do(func() {
		if err != nil {
			watcher.done <- err
		}
		close(watcher.changes)
		close(watcher.done)
	})
}

type uciWatcherBatch struct {
	paths []string
}

type uciWatcherWallClock struct{}

func (uciWatcherWallClock) NewTimer(delay time.Duration) UCIWatcherTimer {
	return uciWatcherWallTimer{timer: time.NewTimer(delay)}
}

type uciWatcherWallTimer struct {
	timer *time.Timer
}

func (timer uciWatcherWallTimer) C() <-chan time.Time {
	return timer.timer.C
}

func (timer uciWatcherWallTimer) Stop() bool {
	return timer.timer.Stop()
}

func uciWatcherPathWithin(root, candidate string) bool {
	if candidate == "" {
		return false
	}
	relativePath, err := filepath.Rel(root, filepath.Clean(candidate))
	return err == nil && (relativePath == "." || uciWatcherRelativePathIsSafe(relativePath))
}

func uciWatcherRelativePathIsSafe(relativePath string) bool {
	return relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relativePath)
}
