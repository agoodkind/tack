// Package environment reads required environment variables for image operations.
package environment

import (
	"errors"
	"os"
)

// ErrUnset reports that the process does not define a required environment variable.
var ErrUnset = errors.New("required environment variable is unset")

// Required returns the value of an environment variable, including an empty value, or [ErrUnset] if the process does not define it.
func Required(key string) (string, error) {
	value, present := os.LookupEnv(key)
	if !present {
		return "", ErrUnset
	}
	return value, nil
}
