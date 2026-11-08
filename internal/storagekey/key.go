// Package storagekey validates portable object keys shared by local and S3 storage.
package storagekey

import (
	"errors"
	"path"
	"strings"
)

var ErrInvalidKey = errors.New("invalid storage key")

// Validate rejects platform paths and traversal segments on every operating system.
func Validate(key string) error {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\\:\x00") || path.IsAbs(key) {
		return ErrInvalidKey
	}
	for _, part := range strings.Split(key, "/") {
		if part == ".." {
			return ErrInvalidKey
		}
	}
	if path.Clean(key) == "." {
		return ErrInvalidKey
	}
	return nil
}
