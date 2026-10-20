package springinspector

import (
	"context"
	"errors"
	"testing"
)

// hostileRepository returns caller-independent bytes to test post-read source-set validation.
type hostileRepository struct{ files map[string][]byte }

// ReadSnapshot deliberately ignores requested roots so the inspector must reject excess authority.
func (r hostileRepository) ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error) {
	return r.files, nil
}

// TestReviewRootFrameworkAndReturnedScopeAreSealed closes REV-1 with digest and hostile repository adversaries.
func TestReviewRootFrameworkAndReturnedScopeAreSealed(t *testing.T) {
	files := map[string][]byte{"src/main/java/demo/C.java": []byte("package demo; class C {}")}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	first := testConfig()
	a, err := NewInput(snapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	second := testConfig()
	second.RootIdentity = "checkout-other"
	b, err := NewInput(snapshot, second)
	if err != nil || a.Digest() == b.Digest() {
		t.Fatal("root identity was not sealed")
	}
	third := testConfig()
	third.FrameworkIdentities = append([]string(nil), third.FrameworkIdentities[:len(third.FrameworkIdentities)-1]...)
	if _, err = NewInput(snapshot, third); err == nil {
		t.Fatal("partial framework contract accepted")
	}
	_, err = FreezeConfiguredRepository(context.Background(), hostileRepository{map[string][]byte{"outside/Evil.java": []byte("class Evil {}")}}, "C:/opaque", testConfig())
	if err == nil {
		t.Fatal("repository returned an out-of-scope path")
	}
}

// TestReviewAmbiguousImportsAndDuplicateTypesNeverChooseByOrder closes REV-2 with symbol-candidate collisions.
func TestReviewAmbiguousImportsAndDuplicateTypesNeverChooseByOrder(t *testing.T) {
	ambiguous := []byte(`package demo; import a.RestController; import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; @RestController class C {@GetMapping("/x") String x(){return "";}}`)
	snapshot, _ := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/C.java": ambiguous})
	input, _ := NewInput(snapshot, testConfig())
	_, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	var inspection *Error
	if !errors.As(err, &inspection) || inspection.Category != CategoryInsufficientContext {
		t.Fatalf("ambiguous import selected a winner: %v", err)
	}

	files := map[string][]byte{
		"src/main/java/demo/C.java":     []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="application/json") Item x(){return null;}}`),
		"src/main/java/demo/Item.java":  []byte(`package demo; public record Item(String a){}`),
		"src/main/java/demo/Other.java": []byte(`package demo; record Item(String b){}`),
	}
	result, _ := inspectFixture(t, files)
	if len(result.Snapshot.Operations[0].Responses["200"].Content) != 0 {
		t.Fatal("duplicate FQCN produced a schema")
	}
	if !hasDiagnostic(result.Diagnostics, DiagnosticUnsupported) {
		t.Fatal("duplicate FQCN had no diagnostic")
	}
	wildcards := []byte(`package demo; import org.springframework.web.bind.annotation.*; import hostile.annotations.*; @RestController class C {@GetMapping("/x") String x(){return "";}}`)
	snapshot, _ = NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/W.java": wildcards})
	input, _ = NewInput(snapshot, testConfig())
	if _, err = (Inspector{}).Inspect(context.Background(), input, "spring"); !errors.As(err, &inspection) || inspection.Category != CategoryInsufficientContext {
		t.Fatalf("ambiguous wildcard selected a winner: %v", err)
	}
	duplicateControllers := map[string][]byte{
		"src/main/java/demo/A.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class Same {@GetMapping("/a") String x(){return "";}}`),
		"src/main/java/demo/B.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class Same {@GetMapping("/b") String x(){return "";}}`),
	}
	snapshot, _ = NewSnapshot("repo", testRevision, duplicateControllers)
	input, _ = NewInput(snapshot, testConfig())
	if _, err = (Inspector{}).Inspect(context.Background(), input, "spring"); !errors.As(err, &inspection) || inspection.Category != CategoryInsufficientContext {
		t.Fatalf("duplicate controller selected a winner: %v", err)
	}
}

// TestReviewAliasConflictAndConstantConcatenation closes REV-3 without choosing an arbitrary alias.
func TestReviewAliasConflictAndConstantConcatenation(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C { static final String PREFIX="/api"; @GetMapping(path=PREFIX+"/ok") String ok(){return "";} @GetMapping(path="/a",value="/b") String bad(){return "";} }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	if len(result.Snapshot.Operations) != 1 || result.Snapshot.Operations[0].Key.Path != "/api/ok" {
		t.Fatalf("aliases/constants=%+v", result.Snapshot.Operations)
	}
	if !hasDiagnostic(result.Diagnostics, DiagnosticDynamicMapping) {
		t.Fatal("alias conflict had no diagnostic")
	}
}

// TestReviewControllerResponseBodyIsResolvedPerMethod closes REV-4 for mixed view/body controllers.
func TestReviewControllerResponseBodyIsResolvedPerMethod(t *testing.T) {
	source := []byte(`package demo; import org.springframework.stereotype.Controller; import org.springframework.web.bind.annotation.*; import org.springframework.http.ResponseEntity; @Controller class C { @GetMapping("/body") @ResponseBody String body(){return "ok";} @GetMapping("/view") String view(){return "page";} @GetMapping("/entity") ResponseEntity<String> entity(){return ResponseEntity.ok("ok");} }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	if len(result.Snapshot.Operations) != 2 {
		t.Fatalf("mixed controller operations=%+v", result.Snapshot.Operations)
	}
}

// TestReviewDTOAndWrapperIdentityRemainDemonstrable closes REV-5 for private fields and local lookalikes.
func TestReviewDTOAndWrapperIdentityRemainDemonstrable(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C { @GetMapping(path="/x",produces="application/json") LocalList x(){return null;} } class LocalList { private String secret; public String visible; } class List<T> { public T forged; }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	schema := result.Snapshot.Operations[0].Responses["200"].Content["application/json"].Schema
	if schema == nil || schema.Properties["visible"] == nil || schema.Properties["secret"] != nil || schema.Type != "object" {
		t.Fatalf("unsafe DTO projection=%+v", schema)
	}
}

// TestReviewResponseEntityBranchesAndDeadCalls closes REV-6 by retaining branch selectors only from returns.
func TestReviewResponseEntityBranchesAndDeadCalls(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; import org.springframework.http.ResponseEntity; @RestController class C { @GetMapping("/x") ResponseEntity<String> x(boolean found){ ResponseEntity.noContent(); if(found) return ResponseEntity.ok("x"); return ResponseEntity.status(404).build(); } }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	responses := result.Snapshot.Operations[0].Responses
	if len(responses) != 2 || responses["200"].Content == nil || responses["404"].Content == nil {
		t.Fatalf("branch responses=%+v", responses)
	}
	if _, exists := responses["204"]; exists {
		t.Fatal("dead/non-returned builder became a response")
	}
	lookalike := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C { @GetMapping("/fake") ResponseEntity<String> fake(){return new ResponseEntity<String>();} } class ResponseEntity<T> { public T value; }`)
	result, _ = inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": lookalike})
	if _, exists := result.Snapshot.Operations[0].Responses["default"]; exists {
		t.Fatal("local ResponseEntity gained Spring semantics")
	}
}

// TestReviewTokenLimitPropagatesAfterValidUnit closes REV-7 without reclassifying global exhaustion as syntax.
func TestReviewTokenLimitPropagatesAfterValidUnit(t *testing.T) {
	files := map[string][]byte{"src/main/java/a/A.java": []byte(`package a; class A {}`), "src/main/java/z/Z.java": []byte(`package z; class Z { int a; int b; int c; int d; }`)}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	config := testConfig()
	config.Limits.MaxTokens = 10
	input, _ := NewInput(snapshot, config)
	_, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	var inspection *Error
	if !errors.As(err, &inspection) || inspection.Category != CategoryLimitExceeded {
		t.Fatalf("token exhaustion was not propagated: %v", err)
	}
}

// TestReviewSchemaAndSecurityOmissionsProduceSafeDiagnostics closes REV-8 with missing/custom/dynamic facts.
func TestReviewSchemaAndSecurityOmissionsProduceSafeDiagnostics(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; import org.springframework.security.access.prepost.PreAuthorize; @RestController class C { @GetMapping(path="/x",produces="application/json") @PreAuthorize(policy()) Missing x(){return null;} }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	if !hasDiagnostic(result.Diagnostics, DiagnosticMissingType) || !hasDiagnostic(result.Diagnostics, DiagnosticPartial) {
		t.Fatalf("diagnostics=%+v", result.Diagnostics)
	}
	lombok := []byte(`package demo; import lombok.Data; import org.springframework.web.bind.annotation.*; @RestController class C { @GetMapping(path="/l",produces="application/json") DTO x(){return null;} } @Data class DTO { private String secret; }`)
	result, _ = inspectFixture(t, map[string][]byte{"src/main/java/demo/L.java": lombok})
	if !hasDiagnostic(result.Diagnostics, DiagnosticUnsupported) || len(result.Snapshot.Operations[0].Responses["200"].Content) != 0 {
		t.Fatalf("Lombok shape was not diagnosed and omitted: %+v", result.Diagnostics)
	}
}

// hasDiagnostic reports whether the bounded diagnostic set contains one closed code.
func hasDiagnostic(values []Diagnostic, code DiagnosticCode) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
