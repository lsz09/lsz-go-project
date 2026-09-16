// Package jobqueue defines the infrastructure-neutral contract used to queue Jobs.
package jobqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"cloudqueue/internal/job"
)

var ErrInvalidMessage = errors.New("invalid job queue message")

// Message identifies the Job that a Worker must load and process.
type Message struct {
	JobID string `json:"job_id"`
}

// NewMessage creates a validated queue Message without changing the provided ID.
func NewMessage(jobID string) (Message, error) {
	message := Message{JobID: jobID}
	if err := message.Validate(); err != nil {
		return Message{}, err
	}
	return message, nil
}

// Validate applies the same Job ID rules used by the PostgreSQL Repository.
func (m Message) Validate() error {
	if err := job.ValidateID(m.JobID); err != nil {
		return invalidMessage("validate job queue message", "job ID is invalid", err)
	}
	return nil
}

// Encode validates a Message and serializes only its stable public contract.
func Encode(message Message) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, fmt.Errorf("encode job queue message: %w", err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, invalidMessage("encode job queue message", "encode JSON", err)
	}
	return encoded, nil
}

// Decode strictly parses one JSON object and validates its Job ID.
func Decode(data []byte) (Message, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Message{}, invalidMessage("decode job queue message", "body is required", io.EOF)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var message Message
	if err := decoder.Decode(&message); err != nil {
		return Message{}, invalidMessage("decode job queue message", "decode JSON", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Message{}, invalidMessage("decode job queue message", "JSON must contain one object", err)
	}
	if err := message.Validate(); err != nil {
		return Message{}, fmt.Errorf("decode job queue message: %w", err)
	}
	return message, nil
}

// ensureJSONEnd rejects a second value after the message object.
func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("additional JSON value")
}

// invalidMessage preserves causes for errors.Is/As without printing untrusted body details.
func invalidMessage(operation string, detail string, cause error) error {
	causes := []error{ErrInvalidMessage, job.ErrInvalidInput}
	if cause != nil {
		causes = append(causes, &safeCause{description: detail, cause: cause})
	}
	return fmt.Errorf("%s: %w", operation, errors.Join(causes...))
}

// safeCause exposes an underlying error to errors.Is/As while sanitizing Error output.
type safeCause struct {
	description string
	cause       error
}

func (e *safeCause) Error() string {
	return e.description
}

func (e *safeCause) Unwrap() error {
	return e.cause
}
