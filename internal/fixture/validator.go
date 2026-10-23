package fixture

import (
	"encoding/json"
	"sort"

	"github.com/asierhr/featureProof/internal/specification"
)

// ValidateSet deep-copies declarations, validates the full graph and reference closure, and derives stable orders.
func ValidateSet(fixtures []specification.Fixture) (Set, error) {
	copySet := cloneFixtures(fixtures)
	byID := make(map[string]specification.Fixture, len(copySet))
	declaration := make(map[string]int, len(copySet))
	for i, item := range copySet {
		if item.ID == "" || byID[item.ID].ID != "" {
			return Set{}, &Error{Category: CategoryInvalid, Phase: "graph", FixtureID: item.ID}
		}
		byID[item.ID], declaration[item.ID] = item, i
	}
	for _, item := range copySet {
		seen := map[string]bool{}
		for _, dependency := range item.DependsOn {
			if dependency == item.ID || seen[dependency] || byID[dependency].ID == "" {
				return Set{}, &Error{Category: CategoryInvalid, Phase: "graph", FixtureID: item.ID}
			}
			seen[dependency] = true
		}
	}
	order, err := stableTopological(copySet, declaration)
	if err != nil {
		return Set{}, err
	}
	for _, item := range copySet {
		closure := dependencyClosure(item.ID, byID)
		if err := validateOperationReferences(item.Create, item.ID, closure, byID, false); err != nil {
			return Set{}, err
		}
		if err := validateOperationReferences(item.Cleanup, item.ID, closure, byID, true); err != nil {
			return Set{}, err
		}
	}
	reverse := append([]string(nil), order...)
	for left, right := 0, len(reverse)-1; left < right; left, right = left+1, right-1 {
		reverse[left], reverse[right] = reverse[right], reverse[left]
	}
	return Set{fixtures: copySet, createOrder: order, cleanupOrder: reverse}, nil
}

// stableTopological implements Kahn ordering with declaration-order tie breaking.
func stableTopological(fixtures []specification.Fixture, declaration map[string]int) ([]string, error) {
	degree, children := map[string]int{}, map[string][]string{}
	for _, item := range fixtures {
		degree[item.ID] = len(item.DependsOn)
		for _, dependency := range item.DependsOn {
			children[dependency] = append(children[dependency], item.ID)
		}
	}
	ready := []string{}
	for _, item := range fixtures {
		if degree[item.ID] == 0 {
			ready = append(ready, item.ID)
		}
	}
	order := make([]string, 0, len(fixtures))
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return declaration[ready[i]] < declaration[ready[j]] })
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)
		for _, child := range children[current] {
			degree[child]--
			if degree[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if len(order) != len(fixtures) {
		return nil, &Error{Category: CategoryInvalid, Phase: "graph_cycle", FixtureID: firstCyclic(fixtures, degree)}
	}
	return order, nil
}

// firstCyclic selects a stable declaration-order error location for cyclic graphs.
func firstCyclic(fixtures []specification.Fixture, degree map[string]int) string {
	for _, item := range fixtures {
		if degree[item.ID] > 0 {
			return item.ID
		}
	}
	return ""
}

// dependencyClosure returns all explicitly reachable dependencies and never infers edges from references.
func dependencyClosure(id string, byID map[string]specification.Fixture) map[string]bool {
	out := map[string]bool{}
	var visit func(string)
	visit = func(current string) {
		for _, dependency := range byID[current].DependsOn {
			if !out[dependency] {
				out[dependency] = true
				visit(dependency)
			}
		}
	}
	visit(id)
	return out
}

// validateOperationReferences checks scope, capture existence, and exact declared types for every recursive input reference.
func validateOperationReferences(operation specification.FixtureOperation, self string, closure map[string]bool, byID map[string]specification.Fixture, cleanup bool) error {
	return walkInput(operation.Input, func(reference specification.FixtureReference) error {
		if reference.FixtureID == self && !cleanup || reference.FixtureID != self && !closure[reference.FixtureID] {
			return &Error{Category: CategoryInvalid, Phase: "reference_scope", FixtureID: self}
		}
		for _, capture := range byID[reference.FixtureID].Captures {
			if capture.Name == reference.Output && capture.ValueType == reference.ValueType {
				return nil
			}
		}
		return &Error{Category: CategoryInvalid, Phase: "reference_capture", FixtureID: self}
	})
}

// walkInput visits every reference leaf in deterministic key order.
func walkInput(input specification.FixtureInput, visit func(specification.FixtureReference) error) error {
	for _, values := range []map[string]specification.FixtureValue{input.PathParameters, input.Query} {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := walkValue(values[key], visit); err != nil {
				return err
			}
		}
	}
	if input.Body != nil {
		return walkValue(*input.Body, visit)
	}
	return nil
}

// walkValue traverses a strict recursive input union without evaluating literals.
func walkValue(value specification.FixtureValue, visit func(specification.FixtureReference) error) error {
	if value.Kind == "reference" && value.Reference != nil {
		return visit(*value.Reference)
	}
	if value.Kind == "object" {
		keys := make([]string, 0, len(value.Object))
		for key := range value.Object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := walkValue(value.Object[key], visit); err != nil {
				return err
			}
		}
	}
	if value.Kind == "array" {
		for _, child := range value.Array {
			if err := walkValue(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// cloneFixtures uses JSON-compatible reconstruction to isolate every nested slice, map, pointer, and literal.
func cloneFixtures(fixtures []specification.Fixture) []specification.Fixture {
	raw, _ := json.Marshal(fixtures)
	var out []specification.Fixture
	_ = json.Unmarshal(raw, &out)
	return out
}

// fixtureByID returns an isolated declaration by exact case-sensitive identifier.
func fixtureByID(fixtures []specification.Fixture, id string) (specification.Fixture, bool) {
	for _, item := range fixtures {
		if item.ID == id {
			return cloneFixtures([]specification.Fixture{item})[0], true
		}
	}
	return specification.Fixture{}, false
}
