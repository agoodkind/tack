// Package environment reads required environment variables for image
// operations.
package environment

import (
	"errors"
	"os"
)

// ErrUnset reports that the process does not define a required
// environment variable.
var ErrUnset = errors.New("required environment variable is unset")

// Required returns the value of an environment variable, including an empty
// value.
//
// The function returns [ErrUnset] if the process does not define the
// variable. An empty value differs from an unset variable because
// [os.LookupEnv] reports whether the key exists.
//
// For example, IMAGE_ARCH can produce these results:
//
//	IMAGE_ARCH=arm64: "arm64", nil
//	IMAGE_ARCH=:      "", nil
//	IMAGE_ARCH unset: "", ErrUnset
//
// For example, a caller can read IMAGE_ARCH:
//
//	value, err := environment.Required("IMAGE_ARCH")
func Required(key string) (string, error) {
	value, present := os.LookupEnv(key)
	if !present {
		return "", ErrUnset
	}

	return value, nil
}
