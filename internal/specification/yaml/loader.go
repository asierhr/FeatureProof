// Package specyaml loads versioned YAML specifications from local files.
package specyaml

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/asierhr/featureProof/internal/specification"
	"go.yaml.in/yaml/v3"
)

var supportedVersions = []int{1}
var errorLocationPattern = regexp.MustCompile(`line ([0-9]+)(: column ([0-9]+))?`)

type documentV1 struct {
	Version     int            `yaml:"version"`
	Feature     featureV1      `yaml:"feature"`
	Target      targetV1       `yaml:"target"`
	Constraints []constraintV1 `yaml:"constraints"`
	Limits      limitsV1       `yaml:"limits"`
}

type featureV1 struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

type targetV1 struct {
	ID      string `yaml:"id"`
	BaseURL string `yaml:"base_url"`
}

type constraintV1 struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	TargetID    string `yaml:"target_id"`
}

type limitsV1 struct {
	MaxSteps     int `yaml:"max_steps"`
	MaxToolCalls int `yaml:"max_tool_calls"`
	MaxRetries   int `yaml:"max_retries"`
}

// Load reads and validates one version 1 YAML specification from path.
func Load(ctx context.Context, path string) (specification.Specification, error) {
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return specification.Specification{}, fmt.Errorf("read specification %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}

	root, err := decodeOne(data)
	if err != nil {
		return specification.Specification{}, err
	}
	if err := checkMapping(root, "", nil); err != nil {
		return specification.Specification{}, err
	}
	version, err := versionOf(root)
	if err != nil {
		return specification.Specification{}, err
	}
	if version != 1 {
		return specification.Specification{}, &specification.UnsupportedVersionError{Received: version, Supported: append([]int(nil), supportedVersions...)}
	}
	if err := checkMapping(root, "", rootFields); err != nil {
		return specification.Specification{}, err
	}
	if err := checkV1Fields(root); err != nil {
		return specification.Specification{}, err
	}

	var external documentV1
	if err := root.Decode(&external); err != nil {
		return specification.Specification{}, syntaxError(err, root.Line, root.Column)
	}
	internal := translate(external)
	if err := specification.Validate(internal); err != nil {
		return specification.Specification{}, err
	}
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}
	return internal, nil
}

func decodeOne(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, syntaxError(errors.New("empty document"), 1, 1)
		}
		return nil, syntaxError(err, 0, 0)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		line, column := document.Line, document.Column
		if len(document.Content) > 0 {
			line, column = document.Content[0].Line, document.Content[0].Column
		}
		return nil, syntaxError(errors.New("document root must be a mapping"), line, column)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, syntaxError(err, 0, 0)
		}
		return nil, syntaxError(errors.New("multiple YAML documents are not allowed"), extra.Line, extra.Column)
	}
	return document.Content[0], nil
}

func syntaxError(cause error, line, column int) error {
	if line == 0 {
		matches := errorLocationPattern.FindStringSubmatch(cause.Error())
		if len(matches) > 1 {
			line, _ = strconv.Atoi(matches[1])
		}
		if len(matches) > 3 && matches[3] != "" {
			column, _ = strconv.Atoi(matches[3])
		}
	}
	if line == 0 {
		line = 1
	}
	return &specification.ValidationError{Category: specification.CategorySyntax, Line: line, Column: column, Cause: fmt.Errorf("decode YAML: %w", cause)}
}

var rootFields = map[string]struct{}{
	"version": {}, "feature": {}, "target": {}, "constraints": {}, "limits": {},
}

func checkMapping(node *yaml.Node, path string, allowed map[string]struct{}) error {
	if node.Kind != yaml.MappingNode {
		return syntaxError(errors.New("expected mapping"), node.Line, node.Column)
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		field := key.Value
		if path != "" {
			field = path + "." + field
		}
		if _, exists := seen[key.Value]; exists {
			return &specification.ValidationError{Category: specification.CategorySyntax, Field: field, Line: key.Line, Column: key.Column, Cause: errors.New("duplicate mapping key")}
		}
		seen[key.Value] = struct{}{}
		if _, exists := allowed[key.Value]; allowed != nil && !exists {
			return &specification.ValidationError{Category: specification.CategoryUnknownField, Field: field, Line: key.Line, Column: key.Column}
		}
	}
	return nil
}

func requireKeys(node *yaml.Node, path string, required ...string) error {
	present := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		present[node.Content[index].Value] = struct{}{}
	}
	for _, key := range required {
		if _, exists := present[key]; !exists {
			field := key
			if path != "" {
				field = path + "." + key
			}
			return &specification.ValidationError{Category: specification.CategoryRequired, Field: field}
		}
	}
	return nil
}

func requireKind(node *yaml.Node, kind yaml.Kind, field string) error {
	if node.Kind != kind {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func requireString(node *yaml.Node, field string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	return nil
}

func requireInteger(node *yaml.Node, field string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column, Cause: err}
	}
	return nil
}

func checkStringFields(node *yaml.Node, path string, fields ...string) error {
	for _, field := range fields {
		if err := requireString(mappingValue(node, field), path+"."+field); err != nil {
			return err
		}
	}
	return nil
}

func checkIntegerFields(node *yaml.Node, path string, fields ...string) error {
	for _, field := range fields {
		if err := requireInteger(mappingValue(node, field), path+"."+field); err != nil {
			return err
		}
	}
	return nil
}

func versionOf(root *yaml.Node) (int, error) {
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "version" {
			continue
		}
		value := root.Content[index+1]
		if value.Tag != "!!int" {
			return 0, &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: "version", Line: value.Line, Column: value.Column}
		}
		version, err := strconv.Atoi(value.Value)
		if err != nil {
			return 0, &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: "version", Line: value.Line, Column: value.Column, Cause: err}
		}
		return version, nil
	}
	return 0, &specification.ValidationError{Category: specification.CategoryRequired, Field: "version"}
}

func checkV1Fields(root *yaml.Node) error {
	if err := requireKeys(root, "", "version", "feature", "target", "constraints", "limits"); err != nil {
		return err
	}
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index].Value, root.Content[index+1]
		switch key {
		case "feature":
			if err := requireKind(value, yaml.MappingNode, "feature"); err != nil {
				return err
			}
			if err := checkMapping(value, "feature", map[string]struct{}{"id": {}, "description": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "feature", "id", "description"); err != nil {
				return err
			}
			if err := checkStringFields(value, "feature", "id", "description"); err != nil {
				return err
			}
		case "target":
			if err := requireKind(value, yaml.MappingNode, "target"); err != nil {
				return err
			}
			if err := checkMapping(value, "target", map[string]struct{}{"id": {}, "base_url": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "target", "id", "base_url"); err != nil {
				return err
			}
			if err := checkStringFields(value, "target", "id", "base_url"); err != nil {
				return err
			}
		case "limits":
			if err := requireKind(value, yaml.MappingNode, "limits"); err != nil {
				return err
			}
			if err := checkMapping(value, "limits", map[string]struct{}{"max_steps": {}, "max_tool_calls": {}, "max_retries": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "limits", "max_steps", "max_tool_calls", "max_retries"); err != nil {
				return err
			}
			if err := checkIntegerFields(value, "limits", "max_steps", "max_tool_calls", "max_retries"); err != nil {
				return err
			}
		case "constraints":
			if err := requireKind(value, yaml.SequenceNode, "constraints"); err != nil {
				return err
			}
			for itemIndex, item := range value.Content {
				path := fmt.Sprintf("constraints[%d]", itemIndex)
				if err := requireKind(item, yaml.MappingNode, path); err != nil {
					return err
				}
				if err := checkMapping(item, path, map[string]struct{}{"id": {}, "description": {}, "target_id": {}}); err != nil {
					return err
				}
				if err := requireKeys(item, path, "id", "description", "target_id"); err != nil {
					return err
				}
				if err := checkStringFields(item, path, "id", "description", "target_id"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func translate(external documentV1) specification.Specification {
	constraints := make([]specification.Constraint, len(external.Constraints))
	for index, constraint := range external.Constraints {
		constraints[index] = specification.Constraint{ID: constraint.ID, Description: constraint.Description, TargetID: constraint.TargetID}
	}
	return specification.Specification{
		Version:     external.Version,
		Feature:     specification.Feature{ID: external.Feature.ID, Description: external.Feature.Description},
		Target:      specification.Target{ID: external.Target.ID, BaseURL: external.Target.BaseURL},
		Constraints: constraints,
		Limits:      specification.Limits{MaxSteps: external.Limits.MaxSteps, MaxToolCalls: external.Limits.MaxToolCalls, MaxRetries: external.Limits.MaxRetries},
	}
}
