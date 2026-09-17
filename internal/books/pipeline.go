package books

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Status is the lifecycle state retained for historical book jobs.
type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"

	RetirementFailureReason = "interrupted by retirement"
)

var (
	ErrPlaintextBookIntakeRetired = errors.New("plaintext book intake retired")
	ErrBookWriterNotQuiesced      = errors.New("book writer must be quiesced before retirement")
)

// Job is the historical books_jobs aggregate.
type Job struct {
	ID        int64
	Status    Status
	SourceRef string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store preserves historical book-job status reads.
type Store interface {
	GetStatus(ctx context.Context, id int64) (*Job, error)
}

// ResidualJobStore is the T006b handoff for the one-time, non-destructive
// transition after the existing single-container book writer is quiesced.
type ResidualJobStore interface {
	RetireNonterminal(ctx context.Context, reason string) (int64, error)
}

// ProcessRequest remains only until T006b removes the shared startup field.
type ProcessRequest struct {
	JobID     int64
	SourceRef string
	Content   string
	Project   string
	Author    string
}

// Pipeline is an inert compatibility shell until T006b removes its shared
// startup declaration. It never admits or processes plaintext.
type Pipeline struct{}

func NewPipeline(_ Store, _ any) *Pipeline {
	return &Pipeline{}
}

func (p *Pipeline) Process(_ context.Context, _ ProcessRequest) error {
	return ErrPlaintextBookIntakeRetired
}

// RetireResidualJobs marks only nonterminal rows failed after the caller has
// established the existing single-container writer is not live.
func RetireResidualJobs(ctx context.Context, store ResidualJobStore, writerQuiesced bool) (int64, error) {
	if !writerQuiesced {
		return 0, ErrBookWriterNotQuiesced
	}
	if store == nil {
		return 0, fmt.Errorf("retire residual book jobs: store not configured")
	}
	return store.RetireNonterminal(ctx, RetirementFailureReason)
}

// DocumentPathPrefix remains the stable historical Documents provenance tag.
func DocumentPathPrefix(jobID int64) string {
	return fmt.Sprintf("books/jobs/%d/", jobID)
}
