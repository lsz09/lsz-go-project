package jobqueue

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"cloudqueue/internal/job"
)

const validJobID = "00000000-0000-0000-0000-000000000001"

func TestNewMessage(t *testing.T) {
	message, err := NewMessage(validJobID)
	if err != nil {
		t.Fatalf("new message: %v", err)
	}
	if message.JobID != validJobID {
		t.Fatalf("Job ID changed: %q", message.JobID)
	}

	invalidIDs := []string{
		"",
		"   ",
		"00000000000000000000000000000001",
		"00000000-0000-0000-0000-not-a-uuid00",
		" " + validJobID,
		validJobID + " ",
	}
	for _, id := range invalidIDs {
		message, err := NewMessage(id)
		if !errors.Is(err, ErrInvalidMessage) || !errors.Is(err, job.ErrInvalidInput) {
			t.Errorf("NewMessage(%q): expected both validation causes, got %v", id, err)
		}
		if message != (Message{}) {
			t.Errorf("NewMessage(%q): returned partial message %+v", id, message)
		}
	}
}

func TestEncode(t *testing.T) {
	encoded, err := Encode(Message{JobID: validJobID})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const expected = `{"job_id":"00000000-0000-0000-0000-000000000001"}`
	if string(encoded) != expected {
		t.Fatalf("expected %s, got %s", expected, encoded)
	}

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode encoded message: %v", err)
	}
	if !reflect.DeepEqual(fields, map[string]any{"job_id": validJobID}) {
		t.Fatalf("unexpected message fields: %#v", fields)
	}

	encoded, err = Encode(Message{})
	if encoded != nil || !errors.Is(err, ErrInvalidMessage) || !errors.Is(err, job.ErrInvalidInput) {
		t.Fatalf("expected invalid message error, got %q, %v", encoded, err)
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty"},
		{name: "blank", body: " \r\n\t"},
		{name: "invalid JSON", body: `{"job_id":`},
		{name: "missing job ID", body: `{}`},
		{name: "empty job ID", body: `{"job_id":""}`},
		{name: "invalid UUID", body: `{"job_id":"not-a-uuid"}`},
		{name: "unknown field", body: `{"job_id":"` + validJobID + `","secret":"top-secret-value"}`},
		{name: "trailing object", body: `{"job_id":"` + validJobID + `"} {"secret":"top-secret-value"}`},
		{name: "trailing scalar", body: `{"job_id":"` + validJobID + `"} 42`},
		{name: "array", body: `[{"job_id":"` + validJobID + `"}]`},
		{name: "string", body: `"top-secret-value"`},
		{name: "number", body: `123456789`},
		{name: "null", body: `null`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := Decode([]byte(test.body))
			if !errors.Is(err, ErrInvalidMessage) || !errors.Is(err, job.ErrInvalidInput) {
				t.Fatalf("expected both validation causes, got %v", err)
			}
			if message != (Message{}) {
				t.Fatalf("returned partial message: %+v", message)
			}
			if test.body != "" && strings.Contains(err.Error(), test.body) {
				t.Fatalf("error exposed original body: %v", err)
			}
			if strings.Contains(err.Error(), "top-secret-value") {
				t.Fatalf("error exposed body value: %v", err)
			}
		})
	}
}

func TestDecodeValidMessage(t *testing.T) {
	body := []byte("  \n" + `{"job_id":"` + validJobID + `"}` + "\t\n")
	message, err := Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if message != (Message{JobID: validJobID}) {
		t.Fatalf("unexpected message: %+v", message)
	}
}

func TestDecodePreservesJSONCauseWithoutPrintingIt(t *testing.T) {
	body := []byte(`{"job_id":!}`)
	_, err := Decode(body)
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("expected JSON syntax cause, got %v", err)
	}
	if strings.Contains(err.Error(), string(body)) {
		t.Fatalf("error exposed original body: %v", err)
	}
}
