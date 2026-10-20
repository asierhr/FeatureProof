package springinspector

import "testing"

// TestNestedDTOUnsupportedShapesAreOmitted closes REV-13 for nested Lombok, Jackson, and custom serialization shapes.
func TestNestedDTOUnsupportedShapesAreOmitted(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "lombok_before_method", source: `package demo; import lombok.Data; import org.springframework.web.bind.annotation.*; @RestController class C { @Data static class Reply { private String hidden; } @GetMapping(path="/x",produces="application/json") Reply x(){return null;} }`},
		{name: "lombok_after_method", source: `package demo; import lombok.Value; import org.springframework.web.bind.annotation.*; @RestController class C { @GetMapping(path="/x",produces="application/json") Reply x(){return null;} @Value static class Reply { String hidden; } }`},
		{name: "jackson", source: `package demo; import com.fasterxml.jackson.databind.annotation.JsonSerialize; import org.springframework.web.bind.annotation.*; @RestController class C { @GetMapping(path="/x",produces="application/json") Reply x(){return null;} @JsonSerialize(using=Codec.class) static class Reply { public String forged; } static class Codec {} }`},
		{name: "custom_method", source: `package demo; import org.springframework.web.bind.annotation.*; @RestController class C { static class Reply { public String forged; public String serialize(){return forged;} } @GetMapping(path="/x",produces="application/json") Reply x(){return null;} }`},
		{name: "deep_nested", source: `package demo; import lombok.Data; import org.springframework.web.bind.annotation.*; @RestController class C { static class Envelope { @Data static class Reply { private String hidden; } } @GetMapping(path="/x",produces="application/json") Envelope.Reply x(){return null;} }`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(test.source)})
			if len(result.Snapshot.Operations) != 1 {
				t.Fatalf("operations=%d diagnostics=%+v", len(result.Snapshot.Operations), result.Diagnostics)
			}
			response := result.Snapshot.Operations[0].Responses["200"]
			if len(response.Content) != 0 || !hasDiagnostic(result.Diagnostics, DiagnosticUnsupported) {
				t.Fatalf("unsafe nested shape projected: response=%+v diagnostics=%+v", response, result.Diagnostics)
			}
		})
	}
}

// TestNestedLookalikeAnnotationsDoNotTriggerFrameworkDegradation proves simple local names cannot impersonate Lombok/Jackson.
func TestNestedLookalikeAnnotationsDoNotTriggerFrameworkDegradation(t *testing.T) {
	source := `package demo; import org.springframework.web.bind.annotation.*; @interface Data {} @interface JsonSerialize {} @RestController class C { @GetMapping(path="/x",produces="application/json") Reply x(){return null;} @Data @JsonSerialize static class Reply { public String visible; } }`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
	schema := result.Snapshot.Operations[0].Responses["200"].Content["application/json"].Schema
	if schema == nil || schema.Properties["visible"] == nil {
		t.Fatalf("lookalike annotation suppressed demonstrable schema: %+v diagnostics=%+v", schema, result.Diagnostics)
	}
}
