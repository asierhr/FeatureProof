package openapi

import (
	"context"
	"testing"
)

// TestReducedContextExposesOnlyRetainedOperationProvenance proves size omissions and caller mutation cannot widen model authority.
func TestReducedContextExposesOnlyRetainedOperationProvenance(t *testing.T) {
	root := t.TempDir()
	doc := `{"openapi":"3.1.0","info":{"title":"x","version":"1"},"paths":{"/a":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	writeFixture(t, root, "api.json", doc)
	cfg := testConfig(root)
	contract := loadFixture(t, root, "api.json", cfg)
	request := ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c1", Operations: []OperationKey{{Method: "GET", Path: "/a"}}}}}
	reduced, err := contract.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Constraints[0].ConstraintID = "mutated"
	items := reduced.RetainedOperations()
	if len(items) != 1 || items[0].ConstraintID != "c1" || items[0].Operation.Key != (OperationKey{Method: "GET", Path: "/a"}) {
		t.Fatalf("unexpected provenance: %#v", items)
	}
	items[0].Operation.Key.Path = "/mutated"
	if reduced.RetainedOperations()[0].Operation.Key.Path != "/a" {
		t.Fatal("returned index aliases retained context")
	}
}

// TestReducedContextProvenanceExcludesWholeOperationsOmittedBySize proves original catalog membership cannot restore omitted authority.
func TestReducedContextProvenanceExcludesWholeOperationsOmittedBySize(t *testing.T) {
	root := t.TempDir()
	doc := `{"openapi":"3.1.0","info":{"title":"x","version":"1"},"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}},"/b":{"post":{"responses":{"200":{"description":"ok"}}}}}}`
	writeFixture(t, root, "api.json", doc)
	contract := loadFixture(t, root, "api.json", testConfig(root))
	firstRequest := ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c1", Operations: []OperationKey{{Method: "GET", Path: "/a"}}}}}
	first, err := contract.BuildContext(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c1", Operations: []OperationKey{{Method: "GET", Path: "/a"}, {Method: "POST", Path: "/b"}}}}}
	var both ReducedContext
	found := false
	for budget := len(first.Bytes); budget < len(first.Bytes)+1024; budget++ {
		contract.maxContext = budget
		candidate, buildErr := contract.BuildContext(context.Background(), request)
		if buildErr == nil && len(candidate.RetainedOperations()) == 1 {
			both, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatal("could not construct one-operation size boundary")
	}
	items := both.RetainedOperations()
	if items[0].Operation.Key.Path != "/a" || both.Omissions.SizeLimit != 1 {
		t.Fatalf("retained=%#v omissions=%+v", items, both.Omissions)
	}
}
