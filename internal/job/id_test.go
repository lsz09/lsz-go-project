package job

import (
	"errors"
	"testing"
)

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "valid", id: "00000000-0000-0000-0000-000000000001"},
		{name: "empty", wantErr: true},
		{name: "blank", id: "   ", wantErr: true},
		{name: "missing hyphens", id: "00000000000000000000000000000001", wantErr: true},
		{name: "wrong length", id: "00000000-0000-0000-0000-00000000001", wantErr: true},
		{name: "not UUID", id: "00000000-0000-0000-0000-not-a-uuid00", wantErr: true},
		{name: "leading space", id: " 00000000-0000-0000-0000-000000000001", wantErr: true},
		{name: "trailing space", id: "00000000-0000-0000-0000-000000000001 ", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateID(test.id)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("expected ErrInvalidInput, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate ID: %v", err)
			}
		})
	}
}
