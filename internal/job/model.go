package job

import "time"

type Status string

const (
	StatusPending    Status = "PENDING"
	StatusProcessing Status = "PROCESSING"
	StatusCompleted  Status = "COMPLETED"
	StatusFailed     Status = "FAILED"
)

// Job mirrors the jobs table. Pointers distinguish SQL NULL from empty values.
type Job struct {
	ID           string
	Status       Status
	FileName     string
	FileKey      *string
	ResultKey    *string
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

type CreateParams struct {
	FileName string
	FileKey  *string
}

// ListOptions uses a default limit of 20 when Limit is zero.
type ListOptions struct {
	Limit  int
	Offset int
}
