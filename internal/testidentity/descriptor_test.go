package testidentity

import "testing"

// descriptorFixture returns two safe descriptors in deliberately non-canonical order.
func descriptorFixture() []Descriptor {
	return []Descriptor{
		{ID: "second", Mechanism: MechanismHeader, Operations: []Operation{{Capability: "http.request", TargetID: "target", Method: "POST", Path: "/pets"}}},
		{ID: "first", Mechanism: MechanismBearer, Operations: []Operation{{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/pets/{id}"}, {Capability: "http.request", TargetID: "target", Method: "GET", Path: "/pets"}}},
	}
}

// TestDescriptorSnapshotsCloneCanonicalizeValidateAndDigest covers every immutable descriptor boundary used by workflow admission.
func TestDescriptorSnapshotsCloneCanonicalizeValidateAndDigest(t *testing.T) {
	source := descriptorFixture()
	clone := CloneDescriptors(source)
	clone[0].Operations[0].Path = "/changed"
	if source[0].Operations[0].Path != "/pets" {
		t.Fatal("descriptor clone shared operation storage")
	}
	canonical, err := CanonicalDescriptors(source)
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].ID != "first" || canonical[0].Operations[0].Path != "/pets" {
		t.Fatalf("non-canonical descriptor order: %#v", canonical)
	}
	if ValidateDescriptors(nil) != nil || ValidateDescriptors(canonical) != nil || ValidateDescriptors(source) == nil {
		t.Fatal("descriptor canonical validation mismatch")
	}
	first, err := DescriptorDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DescriptorDigest(canonical)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("descriptor digest mismatch: %q %q %v", first, second, err)
	}
}

// TestCanonicalDescriptorsRejectsMalformedAuthorization covers the closed identity and operation descriptor vocabulary.
func TestCanonicalDescriptorsRejectsMalformedAuthorization(t *testing.T) {
	valid := Descriptor{ID: "identity", Mechanism: MechanismCookie, Operations: []Operation{{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/pets"}}}
	for _, mutate := range []func(*Descriptor){
		func(value *Descriptor) { value.ID = "" },
		func(value *Descriptor) { value.Mechanism = "invented" },
		func(value *Descriptor) { value.Operations = nil },
		func(value *Descriptor) { value.Operations[0].Capability = "probe" },
		func(value *Descriptor) { value.Operations[0].TargetID = "" },
		func(value *Descriptor) { value.Operations[0].Method = "get" },
		func(value *Descriptor) { value.Operations[0].Path = "relative" },
		func(value *Descriptor) { value.Operations[0].Path = "/pets?secret" },
		func(value *Descriptor) { value.Operations = append(value.Operations, value.Operations[0]) },
	} {
		candidate := cloneDescriptor(valid)
		mutate(&candidate)
		if _, err := CanonicalDescriptors([]Descriptor{candidate}); err == nil {
			t.Fatalf("malformed descriptor accepted: %#v", candidate)
		}
	}
	if _, err := CanonicalDescriptors([]Descriptor{valid, valid}); err == nil {
		t.Fatal("duplicate identity accepted")
	}
}
