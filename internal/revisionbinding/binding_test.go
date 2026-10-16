package revisionbinding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedResolver simulates an immutable commit snapshot across mutable ref changes.
type fixedResolver struct {
	revision Revision
	calls    int
}

// Resolve returns one controlled source observation.
func (f *fixedResolver) Resolve(_ context.Context, _, _ string) (Revision, error) {
	f.calls++
	return f.revision, nil
}

// fixedAttestor simulates a trusted configured source, including failures.
type fixedAttestor struct {
	attestation Attestation
	err         error
	calls       int
}

// Attest returns one controlled deployment observation.
func (f *fixedAttestor) Attest(_ context.Context, _ string) (Attestation, error) {
	f.calls++
	return f.attestation, f.err
}

// baseline supplies complete safe identities for model tests.
func baseline() (Revision, Attestation) {
	r := Revision{RepositoryID: "repo-main", CommitOID: strings.Repeat("a", 40)}
	a := Attestation{EnvironmentID: "preview-1", RepositoryID: r.RepositoryID, CommitOID: r.CommitOID, Reference: "deploy-1", ValidUntil: time.Now().Add(time.Hour), Verified: true}
	return r, a
}

// TestBindingRejectsIncompleteAndMalformedIdentity covers R1 and R3 identity invariants.
func TestBindingRejectsIncompleteAndMalformedIdentity(t *testing.T) {
	ctx := context.Background()
	r, a := baseline()
	d := strings.Repeat("0", 64)
	for _, mutate := range []func(*Revision, *Attestation){
		func(r *Revision, _ *Attestation) { r.CommitOID = "abc" },
		func(r *Revision, _ *Attestation) { r.RepositoryID = "https://user:secret@host/repo" },
		func(_ *Revision, a *Attestation) { a.EnvironmentID = "../preview" },
		func(_ *Revision, a *Attestation) { a.CommitOID = strings.Repeat("b", 40) },
		func(_ *Revision, a *Attestation) { a.Verified = false },
	} {
		rr, aa := r, a
		mutate(&rr, &aa)
		if _, err := NewBinding(ctx, "run-1", rr, aa, d, d, d); err == nil {
			t.Fatal("accepted malformed binding")
		}
	}
	b, err := NewBinding(ctx, "run-1", r, a, d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestBindingCancellationAndTamperFailClosed covers R8 without external calls.
func TestBindingCancellationAndTamperFailClosed(t *testing.T) {
	r, a := baseline()
	d := strings.Repeat("1", 64)
	b, err := NewBinding(context.Background(), "run-1", r, a, d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	b.CommitOID = strings.Repeat("b", 40)
	if !IsCategory(b.Verify(context.Background()), BindingIntegrity) {
		t.Fatal("tamper accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !IsCategory(b.Verify(ctx), Cancelled) {
		t.Fatal("cancelled verify accepted")
	}
	resolver := &fixedResolver{revision: r}
	attestor := &fixedAttestor{attestation: a}
	_, _, err = Admit(ctx, Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now())
	if !IsCategory(err, Cancelled) || resolver.calls != 0 || attestor.calls != 0 {
		t.Fatalf("cancelled admission continued: %v", err)
	}
}

// TestEnvironmentMustAttestExactRepositoryAndCommit covers R3's trusted match.
func TestEnvironmentMustAttestExactRepositoryAndCommit(t *testing.T) {
	r, a := baseline()
	resolver := &fixedResolver{revision: r}
	attestor := &fixedAttestor{attestation: a}
	got, proof, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now())
	if err != nil || got != r || proof.Reference != "deploy-1" {
		t.Fatalf("admission=%+v %+v %v", got, proof, err)
	}
	attestor.attestation.CommitOID = strings.Repeat("b", 40)
	if _, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now()); !IsCategory(err, RevisionMismatch) {
		t.Fatal(err)
	}
	attestor.attestation.RepositoryID = "other-repo"
	if _, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now()); !IsCategory(err, RevisionMismatch) {
		t.Fatal(err)
	}
}

// TestAbsentOrUntrustedAttestationStopsExecution covers R3 and R8 fail-closed behavior.
func TestAbsentOrUntrustedAttestationStopsExecution(t *testing.T) {
	r, a := baseline()
	resolver := &fixedResolver{revision: r}
	if _, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, nil, time.Now()); !IsCategory(err, InvalidRevision) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Attestation){
		func(a *Attestation) { a.Verified = false }, func(a *Attestation) { a.ValidUntil = time.Now().Add(-time.Second) }, func(a *Attestation) { a.EnvironmentID = "other" }, func(a *Attestation) { a.Reference = "" },
	} {
		current := a
		mutate(&current)
		_, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, &fixedAttestor{attestation: current}, time.Now())
		if !IsCategory(err, UnverifiedEnvironment) {
			t.Fatal(err)
		}
	}
	_, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, &fixedAttestor{err: errors.New("CANARY-SECRET")}, time.Now())
	if !IsCategory(err, UnverifiedEnvironment) || strings.Contains(err.Error(), "CANARY") {
		t.Fatal(err)
	}
}

// TestBindingHashesAreStableAndSensitiveToSemanticChanges covers R4's canonical digests.
func TestBindingHashesAreStableAndSensitiveToSemanticChanges(t *testing.T) {
	ctx := context.Background()
	first := SafeObject(map[string]SafeValue{"id": PublicString("health"), "limit": PublicInt(2)})
	second := SafeObject(map[string]SafeValue{"limit": PublicInt(2), "id": PublicString("health")})
	a, err := HashSafe(ctx, "constraints", first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashSafe(ctx, "constraints", second)
	if err != nil || a != b {
		t.Fatalf("unstable %s %s %v", a, b, err)
	}
	c, err := HashSafe(ctx, "constraints", SafeObject(map[string]SafeValue{"id": PublicString("health"), "limit": PublicInt(3)}))
	if err != nil || a == c {
		t.Fatal("semantic change ignored")
	}
	if _, err := HashSafe(ctx, "plan", SafeValue{}); !IsCategory(err, BindingIntegrity) {
		t.Fatal("unclassified plan accepted")
	}
}

// TestSecretCanariesNeverEnterBindingOrPublicDigests covers R7's reference boundary.
func TestSecretCanariesNeverEnterBindingOrPublicDigests(t *testing.T) {
	ctx := context.Background()
	a, _ := HashSafe(ctx, "configuration", SafeObject(map[string]SafeValue{"auth_ref": CredentialReference("cred-1"), "timeout": PublicInt(5)}))
	b, _ := HashSafe(ctx, "configuration", SafeObject(map[string]SafeValue{"timeout": PublicInt(5), "auth_ref": CredentialReference("cred-1")}))
	if a != b || strings.Contains(a, "CANARY-SECRET") {
		t.Fatal("secret rotation changed public hash")
	}
	if _, err := HashSafe(ctx, "configuration", SafeObject(map[string]SafeValue{"password": PublicString("CANARY-SECRET")})); !IsCategory(err, BindingIntegrity) {
		t.Fatal("secret key accepted")
	}
}

// TestHashSafeClassifiesNestedValuesAndRejectsUnsafeShapes covers R4 and R7 traversal limits.
func TestHashSafeClassifiesNestedValuesAndRejectsUnsafeShapes(t *testing.T) {
	ctx := context.Background()
	projection := SafeObject(map[string]SafeValue{"checks": SafeArray(SafeObject(map[string]SafeValue{"enabled": PublicBool(true), "name": PublicString("health"), "auth_ref": CredentialReference("cred-1")}))})
	a, err := HashSafe(ctx, "plan", projection)
	if err != nil || len(a) != 64 {
		t.Fatalf("hash=%q %v", a, err)
	}
	b, err := HashSafe(ctx, "plan", SafeObject(map[string]SafeValue{"checks": SafeArray(SafeObject(map[string]SafeValue{"enabled": PublicBool(false), "name": PublicString("health"), "auth_ref": CredentialReference("cred-1")}))}))
	if err != nil || a == b {
		t.Fatal("boolean change ignored")
	}
	for _, candidate := range []SafeValue{SafeArray(SafeValue{}), SafeObject(map[string]SafeValue{"auth_ref": CredentialReference("../secret")}), SafeObject(map[string]SafeValue{"token": PublicString("CANARY")}), PublicString(strings.Repeat("x", 8193))} {
		if _, err := HashSafe(ctx, "plan", candidate); !IsCategory(err, BindingIntegrity) {
			t.Fatalf("unsafe node accepted: %v", err)
		}
	}
	if _, err := HashSafe(ctx, "unknown", projection); !IsCategory(err, BindingIntegrity) {
		t.Fatal("unknown domain accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := HashSafe(cancelled, "plan", projection); !IsCategory(err, Cancelled) {
		t.Fatal("cancelled hash continued")
	}
}

// TestAdmissionStopsAfterDirtyOrMalformedSource covers R1/R2 before attestation work.
func TestAdmissionStopsAfterDirtyOrMalformedSource(t *testing.T) {
	r, a := baseline()
	attestor := &fixedAttestor{attestation: a}
	resolver := &fixedResolver{revision: r}
	r.Dirty = true
	resolver.revision = r
	if _, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now()); !IsCategory(err, DirtyWorktree) || attestor.calls != 0 {
		t.Fatal("dirty source reached attestor")
	}
	r.Dirty = false
	r.CommitOID = "partial"
	resolver.revision = r
	if _, _, err := Admit(context.Background(), Selection{"repo-main", "main", "preview-1"}, resolver, attestor, time.Now()); !IsCategory(err, InvalidRevision) || attestor.calls != 0 {
		t.Fatal("malformed source reached attestor")
	}
}

// TestRecheckDetectsRevisionAndAttestationDrift covers R1/R3 after admission.
func TestRecheckDetectsRevisionAndAttestationDrift(t *testing.T) {
	ctx := context.Background()
	r, a := baseline()
	d := strings.Repeat("1", 64)
	base, err := NewBinding(ctx, "run-1", r, a, d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedResolver{revision: r}
	attestor := &fixedAttestor{attestation: a}
	selected := Selection{RepositoryID: "repo-main", Ref: "main", EnvironmentID: "preview-1"}
	if err := Recheck(ctx, selected, resolver, attestor, time.Now(), base); err != nil {
		t.Fatal(err)
	}
	attestor.attestation.Reference = "new-deploy"
	if err := Recheck(ctx, selected, resolver, attestor, time.Now(), base); !IsCategory(err, RevisionMismatch) {
		t.Fatal("attestation provenance drift accepted")
	}
	attestor.attestation = a
	resolver.revision.CommitOID = strings.Repeat("b", 40)
	if err := Recheck(ctx, selected, resolver, attestor, time.Now(), base); !IsCategory(err, RevisionMismatch) {
		t.Fatal("commit drift accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := Recheck(cancelled, selected, resolver, attestor, time.Now(), base); !IsCategory(err, Cancelled) {
		t.Fatal("cancelled recheck continued")
	}
}

// TestProjectionBoundsAndDomainSeparation covers R4/R8 bounded canonical hashing.
func TestProjectionBoundsAndDomainSeparation(t *testing.T) {
	ctx := context.Background()
	leaf := PublicString("health")
	constraints, err := HashSafe(ctx, "constraints", leaf)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := HashSafe(ctx, "plan", leaf)
	if err != nil || constraints == plan {
		t.Fatal("hash domains collided")
	}
	deep := leaf
	for i := 0; i < 34; i++ {
		deep = SafeArray(deep)
	}
	if _, err := HashSafe(ctx, "plan", deep); !IsCategory(err, BindingIntegrity) {
		t.Fatal("deep projection accepted")
	}
	tooMany := make([]SafeValue, 4097)
	for i := range tooMany {
		tooMany[i] = PublicInt(int64(i))
	}
	if _, err := HashSafe(ctx, "plan", SafeArray(tooMany...)); !IsCategory(err, BindingIntegrity) {
		t.Fatal("unbounded array accepted")
	}
	large := make([]SafeValue, 130)
	for i := range large {
		large[i] = PublicString(strings.Repeat("x", 8192))
	}
	if _, err := HashSafe(ctx, "plan", SafeArray(large...)); !IsCategory(err, BindingIntegrity) {
		t.Fatal("unbounded encoded bytes accepted")
	}
}

// TestStaleApprovalCannotBindChangedPlan covers R4's exact authorized digest.
func TestStaleApprovalCannotBindChangedPlan(t *testing.T) {
	r, a := baseline()
	d := strings.Repeat("0", 64)
	base, _ := NewBinding(context.Background(), "run-1", r, a, d, d, d)
	if _, err := NewPlanBinding(context.Background(), base, 1, d, "approved", false); !IsCategory(err, BindingIntegrity) {
		t.Fatal("unapproved plan bound")
	}
	plan, err := NewPlanBinding(context.Background(), base, 1, d, "approved", true)
	if err != nil || plan.PlanSHA256 != d {
		t.Fatal(err)
	}
}

// TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision covers R5 and R6 batch lineage.
func TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision(t *testing.T) {
	r, a := baseline()
	d := strings.Repeat("0", 64)
	base, _ := NewBinding(context.Background(), "run-1", r, a, d, d, d)
	one, err := NewPlanBinding(context.Background(), base, 1, d, "approval_not_required", false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewPlanBinding(context.Background(), base, 2, strings.Repeat("1", 64), "approved", true)
	if err != nil {
		t.Fatal(err)
	}
	if one.BindingSHA256 != two.BindingSHA256 || one.Batch != 1 || two.Batch != 2 || base.CommitOID != r.CommitOID {
		t.Fatal("base revision changed")
	}
	if err := VerifyPlanChain(context.Background(), base, []PlanBinding{one, two}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlanChain(context.Background(), base, []PlanBinding{two, one}); !IsCategory(err, BindingIntegrity) {
		t.Fatal("reordered chain accepted")
	}
}
