package storage

import "context"

// RunFeedback is one human rating/comment on a run (the collaboration
// layer: shareable run links + explicit thumbs/comment feedback).
type RunFeedback struct {
	RunID     string `json:"runId"`
	Rating    int    `json:"rating"` // 1..5
	Comment   string `json:"comment,omitempty"`
	CreatedAt string `json:"createdAt"` // RFC3339
}

// FeedbackStore is implemented by stores that persist run feedback.
// Stores that don't implement it keep feedback in the server's memory.
type FeedbackStore interface {
	SaveFeedback(ctx context.Context, f *RunFeedback) error
	ListFeedback(ctx context.Context, runID string) ([]*RunFeedback, error)
}
