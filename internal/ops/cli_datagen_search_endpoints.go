package ops

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	datagenSearchEndpointCount     = 2
	datagenSearchTwoProcessTimeout = 60 * time.Second
)

func parseDatagenSearchEndpoints(input datagenSearchInput) ([]string, error) {
	if input.VerifyCohort {
		return nil, errors.New("qa datagen search: endpoints conflict with --verify-cohort")
	}
	if !input.Commit || input.PrepareOnly {
		return nil, errors.New("qa datagen search: endpoints require --commit without --prepare-only")
	}
	values := strings.Split(input.Endpoints, ",")
	endpoints := make([]string, 0, len(values))
	for _, value := range values {
		endpoints = append(endpoints, strings.TrimSpace(value))
	}
	if len(endpoints) != datagenSearchEndpointCount {
		return nil, fmt.Errorf("qa datagen search: endpoints require exactly %d values, got %d", datagenSearchEndpointCount, len(endpoints))
	}
	for _, endpoint := range endpoints {
		if endpoint == "" {
			return nil, fmt.Errorf("qa datagen search: endpoints require %d nonempty values", datagenSearchEndpointCount)
		}
	}
	for _, endpoint := range endpoints {
		if err := validateDatagenSearchEndpoint(endpoint); err != nil {
			return nil, err
		}
	}
	return endpoints, nil
}

func validateDatagenSearchEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("qa datagen search: endpoint %q is not a valid URL", endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("qa datagen search: endpoint %q does not use scheme http or https", endpoint)
	}
	if parsed.Host == "" {
		return fmt.Errorf("qa datagen search: endpoint %q has no host", endpoint)
	}
	return nil
}
