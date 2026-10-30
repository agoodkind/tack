package testenv

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// validateMacRunnerCompose refuses service fields that the native runner cannot apply.
func validateMacRunnerCompose(contents []byte) error {
	var document struct {
		Name     string                          `yaml:"name"`
		Services map[string]map[string]yaml.Node `yaml:"services"`
		Volumes  map[string]yaml.Node            `yaml:"volumes"`
		Networks map[string]yaml.Node            `yaml:"networks"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return macRunnerFailure("parse supported Compose configuration", err)
	}
	service, present := document.Services["tests"]
	if !present {
		return errors.New("require a tests service")
	}
	for field := range service {
		if !slices.Contains([]string{"build", "volumes", "environment", "working_dir", "profiles", "networks"}, field) {
			return fmt.Errorf("unsupported tests service field %s", field)
		}
	}
	if node, present := service["volumes"]; present {
		if err := validateMacRunnerVolumeSyntax(node); err != nil {
			return err
		}
	}
	if node, present := service["build"]; present {
		var build map[string]yaml.Node
		if err := node.Decode(&build); err != nil {
			return macRunnerFailure("parse tests build contract", err)
		}
		for field := range build {
			if !slices.Contains([]string{"context", "dockerfile", "args"}, field) {
				return fmt.Errorf("unsupported tests build field %s", field)
			}
		}
	}
	return nil
}

func validateMacRunnerVolumeSyntax(node yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		node = *node.Alias
	}
	for _, entry := range node.Content {
		if entry.Kind == yaml.AliasNode {
			entry = entry.Alias
		}
		if entry.Kind != yaml.MappingNode {
			continue
		}
		var volume map[string]yaml.Node
		if err := entry.Decode(&volume); err != nil {
			return macRunnerFailure("parse tests volume contract", err)
		}
		if bind, present := volume["bind"]; present {
			var options map[string]yaml.Node
			if err := bind.Decode(&options); err != nil {
				return macRunnerFailure("parse tests bind contract", err)
			}
			if _, present := options["create_host_path"]; present {
				return errors.New("unsupported tests volume field bind.create_host_path")
			}
		}
	}
	return nil
}
