package s4directives

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	DefaultHorizon           = "project"
	DefaultPrivacyClass      = "internal"
	MaxDirectiveTextBytes    = 4096
	MaxSourceTurnBytes       = 8192
	MaxDerivedIntentRunes    = 512
	MinDistillConfidence     = 0.5
	defaultRateLimitAccepted = 10
)

var (
	ErrNoStore                   = errors.New("s4a attention event store not configured")
	ErrProjectRequired           = errors.New("project_required")
	ErrSessionRequired           = errors.New("session_required")
	ErrTextRequired              = errors.New("text_required")
	ErrTextTooLarge              = errors.New("text_too_large")
	ErrSourceTurnTooLarge        = errors.New("source_turn_too_large")
	ErrSourceTurnHashRequired    = errors.New("source_turn_hash_required")
	ErrInvalidSourceTurnHash     = errors.New("invalid_source_turn_hash")
	ErrAgentConfirmationRequired = errors.New("agent_confirmation_required")
	ErrRateLimiterNotConfigured  = errors.New("directive_rate_limiter_not_configured")
	ErrDistillerNotConfigured    = errors.New("directive_distiller_not_configured")
	ErrInvalidHorizon            = errors.New("invalid_horizon")
	ErrInvalidPrivacyClass       = errors.New("invalid_privacy_class")
	ErrDistillEmptyIntent        = errors.New("directive_distill_empty_intent")
	ErrDistillLowConfidence      = errors.New("directive_distill_low_confidence")
)

type Store interface {
	Create(ctx context.Context, event cognitive.AttentionEventRecord) (*StoredAttentionEvent, error)
}

type RememberDirectiveRequest struct {
	Text         string `json:"text"`
	SourceTurn   string `json:"source_turn,omitempty"`
	Horizon      string `json:"horizon,omitempty"`
	PrivacyClass string `json:"privacy_class,omitempty"`
}

type StoredAttentionEvent struct {
	ID             int64     `json:"id"`
	Project        string    `json:"project"`
	SessionID      string    `json:"session_id"`
	SourceTurnHash string    `json:"source_turn_hash"`
	DerivedIntent  string    `json:"derived_intent"`
	AgentConfirmed bool      `json:"agent_confirmed"`
	Horizon        string    `json:"horizon"`
	PrivacyClass   string    `json:"privacy_class"`
	CreatedAt      time.Time `json:"created_at"`
}

type RateLimitError struct {
	SessionID  string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	retryAfter := e.RetryAfter.Round(time.Second)
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	return fmt.Sprintf("rate_limit_exceeded: retry after %s", retryAfter)
}

type (
	distillFunc func(ctx context.Context, raw cognitive.RawSignal) (cognitive.Distilled, error)
	timeSource  func() time.Time
)

type Service struct {
	store   Store
	distill distillFunc
	now     timeSource
	limiter *sessionLimiter
}

var (
	_ cognitive.AttentionEventWriter = (*Service)(nil)
	_ cognitive.DirectiveDistiller   = (*Service)(nil)
)

func NewService(store Store) *Service {
	return &Service{
		store:   store,
		distill: defaultDistill,
		now: func() time.Time {
			return time.Now().UTC()
		},
		limiter: newSessionLimiter(time.Minute, defaultRateLimitAccepted, 2*time.Minute),
	}
}

func (s *Service) RememberDirective(ctx context.Context, project, sessionID string, req RememberDirectiveRequest) (*StoredAttentionEvent, error) {
	if s == nil || s.store == nil {
		return nil, ErrNoStore
	}
	if s.limiter == nil {
		return nil, ErrRateLimiterNotConfigured
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrProjectRequired
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, ErrSessionRequired
	}
	text, err := normalizeRequiredText(req.Text)
	if err != nil {
		return nil, err
	}
	sourceTurn, err := normalizeOptionalText(req.SourceTurn, MaxSourceTurnBytes, ErrSourceTurnTooLarge)
	if err != nil {
		return nil, err
	}
	horizon, err := normalizeHorizon(req.Horizon, true)
	if err != nil {
		return nil, err
	}
	privacyClass, err := normalizePrivacyClass(req.PrivacyClass, true)
	if err != nil {
		return nil, err
	}
	sourceTurnHash := hashSourceMaterial(sourceTurn, text)
	now := s.nowValue()
	reservation, err := s.limiter.Reserve(sessionID, now)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			reservation.Cancel()
		}
	}()

	distilled, err := s.Distill(ctx, cognitive.RawSignal{
		Text:       text,
		SourceHash: sourceTurnHash,
		Context: map[string]string{
			"horizon":       horizon,
			"privacy_class": privacyClass,
		},
	})
	if err != nil {
		return nil, err
	}

	stored, err := s.writeStoredEvent(ctx, cognitive.AttentionEventRecord{
		Project:        project,
		SessionID:      sessionID,
		SourceTurnHash: sourceTurnHash,
		DerivedIntent:  distilled.Intent,
		AgentConfirmed: true,
		Horizon:        distilled.Horizon,
		PrivacyClass:   distilled.Privacy,
	})
	if err != nil {
		return nil, err
	}
	reservation.Commit()
	committed = true
	return stored, nil
}

func (s *Service) WriteAttentionEvent(ctx context.Context, event cognitive.AttentionEventRecord) error {
	_, err := s.writeStoredEvent(ctx, event)
	return err
}

func (s *Service) Distill(ctx context.Context, raw cognitive.RawSignal) (cognitive.Distilled, error) {
	if s == nil {
		return cognitive.Distilled{}, ErrNoStore
	}
	if s.distill == nil {
		return cognitive.Distilled{}, ErrDistillerNotConfigured
	}
	text, err := normalizeRequiredText(raw.Text)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	raw.Text = text
	distilled, err := s.distill(ctx, raw)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	return finalizeDistilled(distilled)
}

func (s *Service) writeStoredEvent(ctx context.Context, event cognitive.AttentionEventRecord) (*StoredAttentionEvent, error) {
	if s == nil || s.store == nil {
		return nil, ErrNoStore
	}
	event.Project = strings.TrimSpace(event.Project)
	if event.Project == "" {
		return nil, ErrProjectRequired
	}
	event.SessionID = strings.TrimSpace(event.SessionID)
	if event.SessionID == "" {
		return nil, ErrSessionRequired
	}
	event.SourceTurnHash = strings.TrimSpace(event.SourceTurnHash)
	if event.SourceTurnHash == "" {
		return nil, ErrSourceTurnHashRequired
	}
	if !IsCanonicalSourceTurnHash(event.SourceTurnHash) {
		return nil, ErrInvalidSourceTurnHash
	}
	if !event.AgentConfirmed {
		return nil, ErrAgentConfirmationRequired
	}
	intent := boundIntent(event.DerivedIntent)
	if intent == "" {
		return nil, ErrDistillEmptyIntent
	}
	horizon, err := normalizeHorizon(event.Horizon, true)
	if err != nil {
		return nil, err
	}
	privacyClass, err := normalizePrivacyClass(event.PrivacyClass, true)
	if err != nil {
		return nil, err
	}
	storedEvent := cognitive.AttentionEventRecord{
		Project:        event.Project,
		SessionID:      event.SessionID,
		SourceTurnHash: event.SourceTurnHash,
		DerivedIntent:  intent,
		AgentConfirmed: true,
		Horizon:        horizon,
		PrivacyClass:   privacyClass,
	}
	return s.store.Create(ctx, storedEvent)
}

func defaultDistill(_ context.Context, raw cognitive.RawSignal) (cognitive.Distilled, error) {
	privacyClass, err := normalizePrivacyClass(raw.Context["privacy_class"], true)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	horizon, err := normalizeHorizon(raw.Context["horizon"], true)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	return cognitive.Distilled{
		Intent:     distilledIntentFromText(raw.Text),
		Horizon:    horizon,
		Privacy:    privacyClass,
		Confidence: 1,
	}, nil
}

func finalizeDistilled(distilled cognitive.Distilled) (cognitive.Distilled, error) {
	intent := boundIntent(distilled.Intent)
	if intent == "" {
		return cognitive.Distilled{}, ErrDistillEmptyIntent
	}
	if distilled.Confidence < MinDistillConfidence {
		return cognitive.Distilled{}, ErrDistillLowConfidence
	}
	horizon, err := normalizeHorizon(distilled.Horizon, true)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	privacyClass, err := normalizePrivacyClass(distilled.Privacy, true)
	if err != nil {
		return cognitive.Distilled{}, err
	}
	return cognitive.Distilled{
		Intent:     intent,
		Horizon:    horizon,
		Privacy:    privacyClass,
		Confidence: distilled.Confidence,
	}, nil
}

func normalizeRequiredText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ErrTextRequired
	}
	if len(text) > MaxDirectiveTextBytes {
		return "", ErrTextTooLarge
	}
	return text, nil
}

func normalizeOptionalText(raw string, maxBytes int, tooLarge error) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > maxBytes {
		return "", tooLarge
	}
	return trimmed, nil
}

func normalizeHorizon(raw string, allowDefault bool) (string, error) {
	horizon := strings.ToLower(strings.TrimSpace(raw))
	if horizon == "" {
		if allowDefault {
			return DefaultHorizon, nil
		}
		return "", ErrInvalidHorizon
	}
	switch horizon {
	case "session", "project", "permanent":
		return horizon, nil
	default:
		return "", ErrInvalidHorizon
	}
}

func normalizePrivacyClass(raw string, allowDefault bool) (string, error) {
	privacyClass := strings.ToLower(strings.TrimSpace(raw))
	if privacyClass == "" {
		if allowDefault {
			return DefaultPrivacyClass, nil
		}
		return "", ErrInvalidPrivacyClass
	}
	switch privacyClass {
	case "public", "internal", "secret":
		return privacyClass, nil
	default:
		return "", ErrInvalidPrivacyClass
	}
}

func boundIntent(raw string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if normalized == "" {
		return ""
	}
	runes := []rune(normalized)
	if len(runes) <= MaxDerivedIntentRunes {
		return normalized
	}
	return strings.TrimSpace(string(runes[:MaxDerivedIntentRunes]))
}

var directiveStopwords = map[string]struct{}{
	"a":        {},
	"an":       {},
	"and":      {},
	"always":   {},
	"as":       {},
	"at":       {},
	"be":       {},
	"but":      {},
	"by":       {},
	"can":      {},
	"for":      {},
	"from":     {},
	"if":       {},
	"in":       {},
	"into":     {},
	"is":       {},
	"it":       {},
	"must":     {},
	"of":       {},
	"on":       {},
	"or":       {},
	"please":   {},
	"remember": {},
	"should":   {},
	"that":     {},
	"the":      {},
	"this":     {},
	"to":       {},
	"with":     {},
	"you":      {},
	"your":     {},
}

func distilledIntentFromText(raw string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), " ")
	if normalized == "" {
		return ""
	}
	tokens := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, skip := directiveStopwords[token]; skip {
			continue
		}
		filtered = append(filtered, token)
	}
	if len(filtered) == 0 {
		filtered = tokens
	}
	if len(filtered) > 16 {
		filtered = filtered[:16]
	}
	intent := strings.Join(filtered, " ")
	if intent == normalized {
		intent = "directive " + intent
	}
	return boundIntent(intent)
}

func hashSourceMaterial(sourceTurn, text string) string {
	materialType := "text"
	material := text
	if strings.TrimSpace(sourceTurn) != "" {
		materialType = "source_turn"
		material = sourceTurn
	}
	sum := sha256.Sum256([]byte(materialType + "\x00" + material))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func IsCanonicalSourceTurnHash(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, ch := range value[len(prefix):] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (s *Service) nowValue() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

type sessionLimiter struct {
	mu        sync.Mutex
	window    time.Duration
	limit     int
	entryTTL  time.Duration
	lastSweep time.Time
	entries   map[string]*sessionLimiterEntry
}

type sessionLimiterEntry struct {
	accepted []time.Time
	inFlight int
	lastSeen time.Time
}

type limiterReservation struct {
	limiter    *sessionLimiter
	sessionID  string
	acceptedAt time.Time
	once       sync.Once
}

func newSessionLimiter(window time.Duration, limit int, entryTTL time.Duration) *sessionLimiter {
	if window <= 0 {
		window = time.Minute
	}
	if limit <= 0 {
		limit = defaultRateLimitAccepted
	}
	if entryTTL <= 0 {
		entryTTL = 2 * window
	}
	return &sessionLimiter{
		window:    window,
		limit:     limit,
		entryTTL:  entryTTL,
		entries:   make(map[string]*sessionLimiterEntry),
		lastSweep: time.Now().UTC(),
	}
}

func (l *sessionLimiter) Reserve(sessionID string, now time.Time) (*limiterReservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	entry := l.entryLocked(sessionID)
	entry.accepted = pruneAccepted(entry.accepted, now, l.window)
	entry.lastSeen = now
	if len(entry.accepted)+entry.inFlight >= l.limit {
		return nil, &RateLimitError{SessionID: sessionID, RetryAfter: l.retryAfterLocked(entry, now)}
	}
	entry.inFlight++
	return &limiterReservation{limiter: l, sessionID: sessionID, acceptedAt: now}, nil
}

func (r *limiterReservation) Commit() {
	if r == nil || r.limiter == nil {
		return
	}
	r.once.Do(func() {
		r.limiter.mu.Lock()
		defer r.limiter.mu.Unlock()
		entry, ok := r.limiter.entries[r.sessionID]
		if !ok {
			entry = &sessionLimiterEntry{}
			r.limiter.entries[r.sessionID] = entry
		}
		if entry.inFlight > 0 {
			entry.inFlight--
		}
		entry.accepted = append(pruneAccepted(entry.accepted, r.acceptedAt, r.limiter.window), r.acceptedAt)
		sort.Slice(entry.accepted, func(i, j int) bool { return entry.accepted[i].Before(entry.accepted[j]) })
		entry.lastSeen = r.acceptedAt
	})
}

func (r *limiterReservation) Cancel() {
	if r == nil || r.limiter == nil {
		return
	}
	r.once.Do(func() {
		r.limiter.mu.Lock()
		defer r.limiter.mu.Unlock()
		entry, ok := r.limiter.entries[r.sessionID]
		if !ok {
			return
		}
		if entry.inFlight > 0 {
			entry.inFlight--
		}
		entry.accepted = pruneAccepted(entry.accepted, r.acceptedAt, r.limiter.window)
		entry.lastSeen = r.acceptedAt
		if entry.inFlight == 0 && len(entry.accepted) == 0 {
			delete(r.limiter.entries, r.sessionID)
		}
	})
}

func (l *sessionLimiter) entryLocked(sessionID string) *sessionLimiterEntry {
	entry, ok := l.entries[sessionID]
	if ok {
		return entry
	}
	entry = &sessionLimiterEntry{}
	l.entries[sessionID] = entry
	return entry
}

func (l *sessionLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	for sessionID, entry := range l.entries {
		entry.accepted = pruneAccepted(entry.accepted, now, l.window)
		if entry.inFlight == 0 && len(entry.accepted) == 0 && now.Sub(entry.lastSeen) >= l.entryTTL {
			delete(l.entries, sessionID)
		}
	}
	l.lastSweep = now
}

func (l *sessionLimiter) retryAfterLocked(entry *sessionLimiterEntry, now time.Time) time.Duration {
	if entry == nil || len(entry.accepted) == 0 {
		return l.window
	}
	oldest := entry.accepted[0]
	retryAfter := oldest.Add(l.window).Sub(now)
	if retryAfter <= 0 {
		return time.Second
	}
	return retryAfter
}

func pruneAccepted(accepted []time.Time, now time.Time, window time.Duration) []time.Time {
	if len(accepted) == 0 {
		return accepted
	}
	cutoff := now.Add(-window)
	idx := 0
	for idx < len(accepted) && accepted[idx].Before(cutoff) {
		idx++
	}
	if idx == 0 {
		return accepted
	}
	return append([]time.Time(nil), accepted[idx:]...)
}
