package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestConcurrentResourceChangeProducesDrift exercises resource and namespace double-read drift detection.
func TestConcurrentResourceChangeProducesDrift(t *testing.T) {
	reader := &fakeReader{deployments: []Deployment{{UID: "deployment-uid", ResourceVersion: "v1"}, {UID: "deployment-uid", ResourceVersion: "v2"}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckDeployment, "deployment")
	if err == nil || result.ErrorClass != harness.ErrorPermanent || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("resource drift not closed: %#v %v", result, err)
	}
	reader = &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "v1"}, namespace: []Namespace{{UID: "namespace-uid", ResourceVersion: "n1"}, {UID: "namespace-uid", ResourceVersion: "n2"}}}
	tool, _ = validTool(t, reader)
	if _, err = execute(t, tool, CheckDeployment, "deployment"); err == nil {
		t.Fatal("namespace drift was accepted")
	}
}

// TestAuthorityReplacementExpiryAndRunMismatchFailBeforeEvidence covers every immutable execution anchor.
func TestAuthorityReplacementExpiryAndRunMismatchFailBeforeEvidence(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		reader := &fakeReader{}
		tool, _ := validTool(t, reader)
		result, err := tool.Execute(context.Background(), harness.ToolCall{RunID: "other-run", Input: map[string]any{"check_kind": string(CheckDeployment), "resource_alias": "deployment"}})
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) || len(reader.calls) != 0 {
			t.Fatalf("run mismatch reached Kubernetes: %#v %#v %v", result, reader.calls, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		reader := &fakeReader{}
		tool, _ := validTool(t, reader)
		tool.clock = fixedClock{testNow.Add(2 * time.Hour)}
		result, err := execute(t, tool, CheckDeployment, "deployment")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) || len(reader.calls) != 0 {
			t.Fatalf("expired authority reached Kubernetes: %#v %#v %v", result, reader.calls, err)
		}
	})
	t.Run("namespace_initial", func(t *testing.T) {
		tool, _ := validTool(t, &fakeReader{namespace: []Namespace{{UID: "replacement", ResourceVersion: "n1"}}})
		result, err := execute(t, tool, CheckDeployment, "deployment")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
			t.Fatalf("replacement namespace accepted: %#v %v", result, err)
		}
	})
	t.Run("namespace_final", func(t *testing.T) {
		tool, _ := validTool(t, &fakeReader{namespace: []Namespace{{UID: "namespace-uid", ResourceVersion: "n1"}, {UID: "replacement", ResourceVersion: "n1"}}, deployment: Deployment{UID: "deployment-uid", ResourceVersion: "d1"}})
		result, err := execute(t, tool, CheckDeployment, "deployment")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
			t.Fatalf("namespace replacement during snapshot accepted: %#v %v", result, err)
		}
	})
	t.Run("pod_owner_initial", func(t *testing.T) {
		tool, _ := validTool(t, &fakeReader{deployment: Deployment{UID: "replacement"}})
		result, err := execute(t, tool, CheckPodRestarts, "pods")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
			t.Fatalf("replacement Pod owner accepted: %#v %v", result, err)
		}
	})
	t.Run("pod_owner_final", func(t *testing.T) {
		tool, _ := validTool(t, &fakeReader{deployments: []Deployment{{UID: "deployment-uid", ResourceVersion: "d1"}, {UID: "replacement", ResourceVersion: "d1"}}, podPages: []PodPage{{Items: []Pod{{UID: "pod", OwnerUID: "deployment-uid"}}}}})
		result, err := execute(t, tool, CheckPodRestarts, "pods")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
			t.Fatalf("Pod owner replacement during snapshot accepted: %#v %v", result, err)
		}
	})
}

// TestResourceReplacementAndOwnerDriftAreIntegrityErrors proves identity beats matching object names.
func TestResourceReplacementAndOwnerDriftAreIntegrityErrors(t *testing.T) {
	cases := []struct {
		name   string
		reader *fakeReader
		check  CheckKind
		alias  string
	}{
		{name: "deployment", reader: &fakeReader{deployment: Deployment{UID: "replacement"}}, check: CheckDeployment, alias: "deployment"},
		{name: "job", reader: &fakeReader{job: Job{UID: "replacement"}}, check: CheckJob, alias: "job"},
		{name: "service", reader: &fakeReader{service: Service{UID: "replacement"}}, check: CheckService, alias: "service"},
		{name: "policy", reader: &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policy: NetworkPolicy{UID: "replacement"}}, check: CheckConnectivity, alias: "app-connectivity"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tool, _ := validTool(t, testCase.reader)
			result, err := execute(t, tool, testCase.check, testCase.alias)
			if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
				t.Fatalf("identity drift accepted: %#v %v", result, err)
			}
		})
	}
}

// TestCollectionLimitsFailClosed covers Pods, EndpointSlices, Events, and final byte limits.
func TestCollectionLimitsFailClosed(t *testing.T) {
	t.Run("pods", func(t *testing.T) {
		reader := &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "one", OwnerUID: "deployment-uid"}, {UID: "two", OwnerUID: "deployment-uid"}}}}}
		tool, _ := validTool(t, reader)
		tool.maxItems = 1
		result, err := execute(t, tool, CheckPodRestarts, "pods")
		if err == nil || result.Output["outcome"].(map[string]any)["truncated"] != true {
			t.Fatalf("pod truncation accepted: %#v %v", result, err)
		}
	})
	t.Run("endpoints", func(t *testing.T) {
		reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Continue: "same"}, {Continue: "same"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckService, "service")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
			t.Fatalf("endpoint cycle accepted: %#v %v", result, err)
		}
	})
	t.Run("events", func(t *testing.T) {
		reader := &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "one", InvolvedUID: "deployment-uid"}, {UID: "two", InvolvedUID: "deployment-uid"}}}}}
		tool, _ := validTool(t, reader)
		tool.maxEvents = 1
		result, err := execute(t, tool, CheckEvents, "events")
		if err == nil || result.Output["outcome"].(map[string]any)["truncated"] != true {
			t.Fatalf("event truncation accepted: %#v %v", result, err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		conditions := make([]Condition, 64)
		for index := range conditions {
			conditions[index] = Condition{Type: "Available", Status: "True", Reason: "Available"}
		}
		reader := &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "v1", Conditions: conditions}}
		tool, _ := validTool(t, reader)
		tool.maxOutputBytes = minimumOutputBytes
		result, err := execute(t, tool, CheckDeployment, "deployment")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
			t.Fatalf("oversized output accepted: %#v %v", result, err)
		}
		if len(mustJSON(result.Output)) > minimumOutputBytes {
			t.Fatalf("truncated envelope exceeded configured bytes: %d", len(mustJSON(result.Output)))
		}
	})
}

// TestMaxOutputBytesBoundsEveryOperationalOutcome proves error paths share the same final encoder.
func TestMaxOutputBytesBoundsEveryOperationalOutcome(t *testing.T) {
	tool, _ := validTool(t, &fakeReader{})
	tool.maxOutputBytes = minimumOutputBytes
	categories := []struct {
		category  OutcomeCategory
		transient bool
	}{{OutcomeInsufficient, false}, {OutcomeAccessDenied, false}, {OutcomeTimeout, true}, {OutcomeTransient, true}, {OutcomeIntegrity, false}}
	for _, testCase := range categories {
		t.Run(string(testCase.category), func(t *testing.T) {
			result := baseResult(CheckDeployment, "deployment")
			for index := 0; index < 20; index++ {
				result.Versions = append(result.Versions, Version{ResourceAlias: "alias", Kind: "Deployment", UIDDigest: strings.Repeat("a", 64), ResourceVersion: strings.Repeat("secret-canary", 40)})
			}
			output, err := tool.operationFailure(testCase.category, testCase.transient, nil, result)
			encoded := mustJSON(output.Output)
			if err == nil || len(encoded) > tool.maxOutputBytes || strings.Contains(encoded, "secret-canary") || output.Output["outcome"].(map[string]any)["category"] != string(testCase.category) || output.Output["outcome"].(map[string]any)["truncated"] != true {
				t.Fatalf("unbounded %s output: bytes=%d output=%s err=%v", testCase.category, len(encoded), encoded, err)
			}
		})
	}
}

// TestPageVersionsArePinnedAndEmptyContinuationFailsClosed covers all paginated collection contracts.
func TestPageVersionsArePinnedAndEmptyContinuationFailsClosed(t *testing.T) {
	t.Run("pods_version_change", func(t *testing.T) {
		reader := &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "one", OwnerUID: "deployment-uid"}}, Continue: "next", ResourceVersion: "list-v1"}, {Items: []Pod{{UID: "two", OwnerUID: "deployment-uid"}}, ResourceVersion: "list-v2"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckPodRestarts, "pods")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) || len(reader.podRequests) != 2 || reader.podRequests[1].ResourceVersion != "list-v1" {
			t.Fatalf("Pod list version not pinned: %#v req=%#v err=%v", result, reader.podRequests, err)
		}
	})
	t.Run("endpoints_empty_continuation", func(t *testing.T) {
		reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Continue: "next", ResourceVersion: "list-v1"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckService, "service")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
			t.Fatalf("empty EndpointSlice page continued: %#v %v", result, err)
		}
	})
	t.Run("endpoints_version_change", func(t *testing.T) {
		reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}, Continue: "next", ResourceVersion: "list-v1"}, {Items: []EndpointSlice{{UID: "two", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}, ResourceVersion: "list-v2"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckService, "service")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) || len(reader.endpointRequests) != 2 || reader.endpointRequests[1].ResourceVersion != "list-v1" {
			t.Fatalf("EndpointSlice list version not pinned: %#v req=%#v err=%v", result, reader.endpointRequests, err)
		}
	})
	t.Run("pods_empty_continuation", func(t *testing.T) {
		reader := &fakeReader{podPages: []PodPage{{Continue: "next", ResourceVersion: "list-v1"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckPodRestarts, "pods")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
			t.Fatalf("empty Pod page continued: %#v %v", result, err)
		}
	})
	t.Run("events_version_change", func(t *testing.T) {
		reader := &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "one", InvolvedUID: "deployment-uid"}}, Continue: "next", ResourceVersion: "list-v1"}, {Items: []Event{{UID: "two", InvolvedUID: "deployment-uid"}}, ResourceVersion: "list-v2"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckEvents, "events")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) || len(reader.eventRequests) != 2 || reader.eventRequests[1].ResourceVersion != "list-v1" {
			t.Fatalf("Event list version not pinned: %#v req=%#v err=%v", result, reader.eventRequests, err)
		}
	})
	t.Run("events_empty_continuation", func(t *testing.T) {
		reader := &fakeReader{eventPages: []EventPage{{Continue: "next", ResourceVersion: "list-v1"}}}
		tool, _ := validTool(t, reader)
		result, err := execute(t, tool, CheckEvents, "events")
		if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
			t.Fatalf("empty Event page continued: %#v %v", result, err)
		}
	})
}

// TestCancellationPreventsEveryNextPageAndAnchorRead verifies cancellation before I/O, not inside adapters.
func TestCancellationPreventsEveryNextPageAndAnchorRead(t *testing.T) {
	tests := []struct {
		name             string
		check            CheckKind
		alias, operation string
		reader           func(context.CancelFunc) *fakeReader
	}{
		{name: "pods", check: CheckPodRestarts, alias: "pods", operation: "pods", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "one", OwnerUID: "deployment-uid"}}, Continue: "next"}, {Items: []Pod{{UID: "two", OwnerUID: "deployment-uid"}}}}, onRecord: func(operation string) {
				if operation == "pods" {
					cancel()
				}
			}}
		}},
		{name: "endpoints", check: CheckService, alias: "service", operation: "endpoints", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}, Continue: "next"}, {Items: []EndpointSlice{{UID: "two", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, onRecord: func(operation string) {
				if operation == "endpoints" {
					cancel()
				}
			}}
		}},
		{name: "events", check: CheckEvents, alias: "events", operation: "events", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "one", InvolvedUID: "deployment-uid"}}, Continue: "next"}, {Items: []Event{{UID: "two", InvolvedUID: "deployment-uid"}}}}, onRecord: func(operation string) {
				if operation == "events" {
					cancel()
				}
			}}
		}},
		{name: "deployment_anchor", check: CheckDeployment, alias: "deployment", operation: "deployment", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{deployment: Deployment{UID: "deployment-uid"}, onRecord: func(operation string) {
				if operation == "deployment" {
					cancel()
				}
			}}
		}},
		{name: "job_anchor", check: CheckJob, alias: "job", operation: "job", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{job: Job{UID: "job-uid"}, onRecord: func(operation string) {
				if operation == "job" {
					cancel()
				}
			}}
		}},
		{name: "pod_owner_anchor", check: CheckPodRestarts, alias: "pods", operation: "deployment", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "one", OwnerUID: "deployment-uid"}}}}, onRecord: func(operation string) {
				if operation == "pods" {
					cancel()
				}
			}}
		}},
		{name: "service_anchor", check: CheckService, alias: "service", operation: "service", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, onRecord: func(operation string) {
				if operation == "endpoints" {
					cancel()
				}
			}}
		}},
		{name: "connectivity_service_anchor", check: CheckConnectivity, alias: "app-connectivity", operation: "service", reader: func(cancel context.CancelFunc) *fakeReader {
			return &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policy: NetworkPolicy{UID: "policy-uid", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("e", 64), Declared: true}, onRecord: func(operation string) {
				if operation == "policy" {
					cancel()
				}
			}}
		}},
		{name: "connectivity_policy_anchor", check: CheckConnectivity, alias: "app-connectivity", operation: "policy", reader: func(cancel context.CancelFunc) *fakeReader {
			serviceReads := 0
			return &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policy: NetworkPolicy{UID: "policy-uid", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("e", 64), Declared: true}, onRecord: func(operation string) {
				if operation == "service" {
					serviceReads++
					if serviceReads == 2 {
						cancel()
					}
				}
			}}
		}},
		{name: "namespace_anchor", check: CheckDeployment, alias: "deployment", operation: "namespace", reader: func(cancel context.CancelFunc) *fakeReader {
			deploymentReads := 0
			return &fakeReader{deployment: Deployment{UID: "deployment-uid"}, onRecord: func(operation string) {
				if operation == "deployment" {
					deploymentReads++
					if deploymentReads == 2 {
						cancel()
					}
				}
			}}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			reader := testCase.reader(cancel)
			tool, _ := validTool(t, reader)
			_, err := tool.Execute(ctx, harness.ToolCall{RunID: tool.target.RunID, Input: map[string]any{"check_kind": string(testCase.check), "resource_alias": testCase.alias}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			count := 0
			for _, call := range reader.calls {
				if call == testCase.operation {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("started %d %s reads after cancellation: %#v", count, testCase.operation, reader.calls)
			}
		})
	}
}

// TestJobAndServiceAnchorDriftRemainInsufficient covers composed-check final anchor reads.
func TestJobAndServiceAnchorDriftRemainInsufficient(t *testing.T) {
	jobReader := &fakeReader{jobs: []Job{{UID: "job-uid", ResourceVersion: "j1"}, {UID: "job-uid", ResourceVersion: "j2"}}}
	tool, _ := validTool(t, jobReader)
	if _, err := execute(t, tool, CheckJob, "job"); err == nil {
		t.Fatal("Job drift was accepted")
	}
	serviceReader := &fakeReader{services: []Service{{UID: "service-uid", ResourceVersion: "s1"}, {UID: "service-uid", ResourceVersion: "s2"}}}
	tool, _ = validTool(t, serviceReader)
	if _, err := execute(t, tool, CheckService, "service"); err == nil {
		t.Fatal("Service drift was accepted")
	}
}

// TestConnectivityPolicyDriftRemainsInsufficient proves the relation is double-read after endpoint aggregation.
func TestConnectivityPolicyDriftRemainsInsufficient(t *testing.T) {
	reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policies: []NetworkPolicy{{UID: "policy-uid", ResourceVersion: "p1", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("e", 64), Declared: true}, {UID: "policy-uid", ResourceVersion: "p2", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("f", 64), Declared: true}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckConnectivity, "app-connectivity")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("policy drift was accepted: %#v %v", result, err)
	}
}

// TestUnknownReasonsAndUndeclaredConnectivityStayDeterministic covers safe fallback vocabulary.
func TestUnknownReasonsAndUndeclaredConnectivityStayDeterministic(t *testing.T) {
	reader := &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "event", InvolvedUID: "deployment-uid", Reason: "secret-canary", LastObserved: testNow}}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckEvents, "events")
	if err != nil || !strings.Contains(mustJSON(result.Output), `"reason":"other"`) || strings.Contains(mustJSON(result.Output), "secret-canary") {
		t.Fatalf("unknown reason leaked: %#v %v", result, err)
	}
	reader = &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policy: NetworkPolicy{UID: "policy-uid", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("f", 64), Declared: true}}
	tool, _ = validTool(t, reader)
	result, err = execute(t, tool, CheckConnectivity, "app-connectivity")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
		t.Fatalf("policy mismatch was not integrity failure: %#v %v", result, err)
	}
}

// TestPortNamesAreCatalogBoundAndTiedCollectionsAreCanonical closes text and ordering boundaries.
func TestPortNamesAreCatalogBoundAndTiedCollectionsAreCanonical(t *testing.T) {
	canaryReader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1", Ports: []ServicePort{{Name: "secret-canary", Protocol: "TCP", Port: 8080}}}}
	tool, _ := validTool(t, canaryReader)
	result, err := execute(t, tool, CheckService, "service")
	if err == nil || strings.Contains(mustJSON(result.Output), "secret-canary") || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
		t.Fatalf("unbound port name crossed output: %#v %v", result, err)
	}
	conditions := []Condition{{Type: "Available", Status: "True", Reason: "Available"}, {Type: "Available", Status: "False", Reason: "BackOff"}}
	firstTool, _ := validTool(t, &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "d1", Conditions: conditions}})
	secondTool, _ := validTool(t, &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "d1", Conditions: []Condition{conditions[1], conditions[0]}}})
	first, firstErr := execute(t, firstTool, CheckDeployment, "deployment")
	second, secondErr := execute(t, secondTool, CheckDeployment, "deployment")
	if firstErr != nil || secondErr != nil || first.Output["snapshot_digest"] != second.Output["snapshot_digest"] {
		t.Fatalf("condition permutation changed snapshot: %#v %#v", first.Output, second.Output)
	}
	events := []Event{{UID: "a", InvolvedUID: "deployment-uid", Type: "Normal", Reason: "BackOff", Count: 2, FirstObserved: testNow.Add(-time.Minute), LastObserved: testNow}, {UID: "b", InvolvedUID: "deployment-uid", Type: "Warning", Reason: "BackOff", Count: 1, FirstObserved: testNow.Add(-2 * time.Minute), LastObserved: testNow}}
	firstTool, _ = validTool(t, &fakeReader{eventPages: []EventPage{{Items: events}}})
	secondTool, _ = validTool(t, &fakeReader{eventPages: []EventPage{{Items: []Event{events[1], events[0]}}}})
	first, firstErr = execute(t, firstTool, CheckEvents, "events")
	second, secondErr = execute(t, secondTool, CheckEvents, "events")
	if firstErr != nil || secondErr != nil || first.Output["snapshot_digest"] != second.Output["snapshot_digest"] {
		t.Fatalf("Event permutation changed snapshot: %#v %#v", first.Output, second.Output)
	}
}

// TestServicePortAuthorityIsExactAndCanonical covers relation admission and order-independent API matching.
func TestServicePortAuthorityIsExactAndCanonical(t *testing.T) {
	if servicePortsMatch([]ServicePort{{Name: "http", Protocol: "TCP", Port: 8080}}, nil) {
		t.Fatal("different port cardinality matched")
	}
	left := []ServicePort{{Name: "metrics", Protocol: "TCP", Port: 9090}, {Name: "http", Protocol: "TCP", Port: 8080}, {Name: "dns", Protocol: "UDP", Port: 8080}}
	right := []ServicePort{left[2], left[0], left[1]}
	if !servicePortsMatch(left, right) {
		t.Fatal("equivalent port sets depended on order")
	}
	right[0].Name = "other"
	if servicePortsMatch(left, right) {
		t.Fatal("different port identity matched")
	}
	target, _ := validTarget(t)
	catalog := validCatalog(t, target)
	catalog.Digest = ""
	catalog.Relationships[0].Port = 9090
	if _, err := SealCatalog(catalog); err == nil {
		t.Fatal("relationship introduced an undeclared Service port")
	}
}

// TestConnectivityMissingEndpointsPolicyAndDigestUseClosedCategories prevents generic relation absence.
func TestConnectivityMissingEndpointsPolicyAndDigestUseClosedCategories(t *testing.T) {
	noEndpoints := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}}
	tool, _ := validTool(t, noEndpoints)
	result, err := execute(t, tool, CheckConnectivity, "app-connectivity")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("zero endpoints were complete: %#v %v", result, err)
	}
	truncatedReader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}, {UID: "two", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}}
	tool, _ = validTool(t, truncatedReader)
	tool.maxItems = 1
	result, err = execute(t, tool, CheckConnectivity, "app-connectivity")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) || result.Output["outcome"].(map[string]any)["truncated"] != true {
		t.Fatalf("truncated endpoints were complete connectivity evidence: %#v %v", result, err)
	}
	base := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}}
	missingPolicy := &policyErrorReader{fakeReader: base, err: &ReaderError{Category: OutcomeAbsent}}
	tool, _ = validTool(t, missingPolicy)
	result, err = execute(t, tool, CheckConnectivity, "app-connectivity")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
		t.Fatalf("missing policy used generic absence: %#v %v", result, err)
	}
}

// TestConfigurationAndCatalogRejectUnsafeShapes exercises fail-closed trusted inputs and safe diagnostics.
func TestConfigurationAndCatalogRejectUnsafeShapes(t *testing.T) {
	target, binding := validTarget(t)
	catalog := validCatalog(t, target)
	configs := []Config{{}, {Target: target, Binding: binding, Catalog: catalog, Timeout: 0, MaxPages: 1, PageSize: 1, MaxItems: 1, MaxEvents: 1, MaxOutputBytes: 1, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: &fakeReader{}}, {Target: target, Binding: binding, Catalog: catalog, Timeout: time.Second, MaxPages: 1, PageSize: 1, MaxItems: 1, MaxEvents: 1, MaxOutputBytes: 1, AllowedReasons: []string{""}, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: &fakeReader{}}}
	for _, config := range configs {
		if _, err := New(config); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	invalidCatalogs := []Catalog{{}, {Version: CatalogVersion, TargetDigest: target.Digest, SelectionDigest: target.SelectionDigest, ProviderVersion: target.ProviderVersion, Namespace: target.EnvironmentRef, NamespaceUID: target.ReservationID, Resources: []Resource{{Alias: "x", Kind: KindDeployment, Name: "x", UID: "x", Checks: []CheckKind{CheckJob}}}}, {Version: CatalogVersion, TargetDigest: target.Digest, SelectionDigest: target.SelectionDigest, ProviderVersion: target.ProviderVersion, Namespace: target.EnvironmentRef, NamespaceUID: target.ReservationID, Resources: []Resource{{Alias: "x", Kind: KindNetworkPolicy, Name: "x", UID: "x", Checks: []CheckKind{CheckDeployment}}}}}
	for _, value := range invalidCatalogs {
		if _, err := SealCatalog(value); err == nil {
			t.Fatal("unsafe catalog accepted")
		}
	}
	if !strings.Contains((&ConfigError{Field: "catalog"}).Error(), "catalog") || !strings.Contains((&InputError{Field: "x", Category: "type"}).Error(), "type") {
		t.Fatal("typed diagnostics lost stable fields")
	}
}

// TestNewDetachesTargetSlicesAndRejectsDuplicateReasons protects immutable runtime authority.
func TestNewDetachesTargetSlicesAndRejectsDuplicateReasons(t *testing.T) {
	target, binding := validTarget(t)
	catalog := validCatalog(t, target)
	config := Config{Target: target, Binding: binding, Identity: validIdentity(t, target), Catalog: catalog, Timeout: time.Second, MaxPages: 1, PageSize: 1, MaxItems: 1, MaxEvents: 1, MaxOutputBytes: 2048, AllowedReasons: []string{"Available"}, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: &fakeReader{}}
	tool, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	target.Capabilities[0] = "mutated"
	target.Network.Hosts[0] = "mutated.example"
	if tool.target.Capabilities[0] != ToolName || tool.target.Network.Hosts[0] != "app.test" {
		t.Fatal("caller mutation changed runtime target authority")
	}
	missingIdentity := config
	missingIdentity.Identity = IdentityBinding{}
	if _, err := New(missingIdentity); err == nil {
		t.Fatal("composition without explicit identity was accepted")
	}
	expanded := config
	expanded.Identity.Rules = append(expanded.Identity.Rules, RBACRule{Scope: "namespace", Resource: "secrets", Verbs: []string{"get"}})
	expanded.Identity.Digest = ""
	expanded.Identity.Digest = digest(expanded.Identity)
	if _, err := New(expanded); err == nil {
		t.Fatal("expanded effective permissions were accepted")
	}
	config.AllowedReasons = []string{"Available", "Available"}
	if _, err := New(config); err == nil {
		t.Fatal("duplicate allowlisted reason was accepted")
	}
}

// TestInputTypeAndRelationshipValidationRejectBeforeIO covers malformed scalar and relation authority.
func TestInputTypeAndRelationshipValidationRejectBeforeIO(t *testing.T) {
	reader := &fakeReader{}
	tool, _ := validTool(t, reader)
	inputs := []map[string]any{{"check_kind": 1, "resource_alias": "deployment"}, {"check_kind": string(CheckDeployment), "resource_alias": 1}, {"check_kind": string(CheckConnectivity), "resource_alias": "missing"}}
	for _, input := range inputs {
		if tool.Validate(input) == nil {
			t.Fatalf("invalid input accepted: %#v", input)
		}
	}
	if len(reader.calls) != 0 {
		t.Fatalf("invalid input reached API: %#v", reader.calls)
	}
}

// TestErrorTaxonomyMapsOfficialKubernetesFailures covers stable non-leaking adapter categories.
func TestErrorTaxonomyMapsOfficialKubernetesFailures(t *testing.T) {
	cases := []struct {
		err       error
		category  OutcomeCategory
		transient bool
	}{
		{err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "secret"), category: OutcomeAbsent},
		{err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "secret", errors.New("secret")), category: OutcomeAccessDenied},
		{err: apierrors.NewServiceUnavailable("secret"), category: OutcomeTransient, transient: true},
		{err: errors.New("secret"), category: OutcomeIntegrity},
	}
	for _, testCase := range cases {
		mapped := classifyAPI(context.Background(), testCase.err)
		var typed *ReaderError
		if !errors.As(mapped, &typed) || typed.Category != testCase.category || typed.Transient != testCase.transient || strings.Contains(mapped.Error(), "secret") {
			t.Fatalf("unsafe mapping: %v", mapped)
		}
	}
}

// TestNamespaceAbsenceIsIntegrityFailure proves the authority anchor cannot be asserted as an ordinary absent workload.
func TestNamespaceAbsenceIsIntegrityFailure(t *testing.T) {
	reader := &fakeReader{err: &ReaderError{Category: OutcomeAbsent}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckDeployment, "deployment")
	if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) {
		t.Fatalf("namespace absence was not integrity failure: %#v %v", result, err)
	}
}

// TestRequiredRBACRulesAreMinimalDetachedAndSecretFree proves composition receives no wildcard or mutation authority.
func TestRequiredRBACRulesAreMinimalDetachedAndSecretFree(t *testing.T) {
	rules := RequiredRBACRules("fp-run")
	if len(rules) != 8 || rules[0].Scope != "cluster" || len(rules[0].ResourceNames) != 1 || rules[0].ResourceNames[0] != "fp-run" {
		t.Fatalf("unexpected namespace anchor rule: %#v", rules)
	}
	for _, rule := range rules {
		if rule.Resource == "secrets" || rule.Resource == "*" || len(rule.Verbs) != 1 || rule.Verbs[0] != "get" && rule.Verbs[0] != "list" {
			t.Fatalf("overbroad RBAC rule: %#v", rule)
		}
	}
	rules[0].Verbs[0] = "delete"
	if RequiredRBACRules("fp-run")[0].Verbs[0] != "get" {
		t.Fatal("caller mutated shared RBAC authority")
	}
}

// TestNewRejectsTargetWithoutKubernetesCapability proves registration cannot widen sealed target authority.
func TestNewRejectsTargetWithoutKubernetesCapability(t *testing.T) {
	target, binding := validTargetWithCapabilities(t, []string{"http.request"})
	reader := &fakeReader{}
	_, err := New(Config{Target: target, Binding: binding, Identity: validIdentity(t, target), Catalog: validCatalog(t, target), Timeout: time.Second, MaxPages: 1, PageSize: 1, MaxItems: 1, MaxEvents: 1, MaxOutputBytes: 2048, AllowedReasons: []string{}, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: reader})
	if err == nil || len(reader.calls) != 0 {
		t.Fatalf("target without %s registered the tool: calls=%#v err=%v", ToolName, reader.calls, err)
	}
}

// TestInvalidCountersAndImpossibleRelationsFailContentFree covers every resource counter boundary and endpoint overflow.
func TestInvalidCountersAndImpossibleRelationsFailContentFree(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	tests := []struct {
		name   string
		check  CheckKind
		alias  string
		reader *fakeReader
	}{
		{name: "deployment_negative", check: CheckDeployment, alias: "deployment", reader: &fakeReader{deployment: Deployment{UID: "deployment-uid", Desired: -1}}},
		{name: "deployment_generation", check: CheckDeployment, alias: "deployment", reader: &fakeReader{deployment: Deployment{UID: "deployment-uid", Generation: 1, ObservedGeneration: 2}}},
		{name: "pod_restart", check: CheckPodRestarts, alias: "pods", reader: &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "pod", OwnerUID: "deployment-uid", Containers: []ContainerStatus{{Role: "application", Restarts: -1}}}}}}}},
		{name: "job_negative", check: CheckJob, alias: "job", reader: &fakeReader{job: Job{UID: "job-uid", Active: -1}}},
		{name: "job_impossible", check: CheckJob, alias: "job", reader: &fakeReader{job: Job{UID: "job-uid", Completions: 1, Succeeded: 2}}},
		{name: "endpoint_negative", check: CheckService, alias: "service", reader: &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: -1}}}}}},
		{name: "endpoint_overflow", check: CheckService, alias: "service", reader: &fakeReader{service: Service{UID: "service-uid"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: maximumInt}, {UID: "two", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}}},
		{name: "event_negative", check: CheckEvents, alias: "events", reader: &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "event", InvolvedUID: "deployment-uid", Count: -1}}}}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			tool, _ := validTool(t, testCase.reader)
			result, err := execute(t, tool, testCase.check, testCase.alias)
			encoded := mustJSON(result.Output)
			if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) || strings.Contains(encoded, "secret-canary") {
				t.Fatalf("invalid counter crossed as evidence: output=%s err=%v", encoded, err)
			}
		})
	}
}

// TestConnectivityIncompleteOutcomesRetainObservedVersions proves partial snapshots remain auditable.
func TestConnectivityIncompleteOutcomesRetainObservedVersions(t *testing.T) {
	baseEndpoints := []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", ResourceVersion: "e1", Ready: 1}}, ResourceVersion: "list-v1"}}
	tests := []struct {
		name          string
		reader        Reader
		configure     func(*Tool)
		category      OutcomeCategory
		requiredKinds []string
	}{
		{name: "zero_endpoints", reader: &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{ResourceVersion: "list-v1"}}}, category: OutcomeInsufficient, requiredKinds: []string{"Service", "EndpointSliceList"}},
		{name: "truncated", reader: &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "one", OwnerUID: "service-uid", ServiceUID: "service-uid", ResourceVersion: "e1", Ready: 1}, {UID: "two", OwnerUID: "service-uid", ServiceUID: "service-uid", ResourceVersion: "e2", Ready: 1}}, ResourceVersion: "list-v1"}}}, configure: func(tool *Tool) { tool.maxItems = 1 }, category: OutcomeInsufficient, requiredKinds: []string{"Service", "EndpointSliceList", "EndpointSlice"}},
		{name: "policy_absent", reader: &policyErrorReader{fakeReader: &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: baseEndpoints}, err: &ReaderError{Category: OutcomeAbsent}}, category: OutcomeIntegrity, requiredKinds: []string{"Service", "EndpointSliceList", "EndpointSlice"}},
		{name: "policy_different", reader: &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: baseEndpoints, policy: NetworkPolicy{UID: "policy-uid", ResourceVersion: "p1", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("f", 64), Declared: true}}, category: OutcomeIntegrity, requiredKinds: []string{"Service", "EndpointSliceList", "EndpointSlice", "NetworkPolicy"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			tool, _ := validTool(t, testCase.reader)
			if testCase.configure != nil {
				testCase.configure(tool)
			}
			result, err := execute(t, tool, CheckConnectivity, "app-connectivity")
			if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(testCase.category) {
				t.Fatalf("unexpected partial connectivity outcome: %#v %v", result, err)
			}
			kinds := outputVersionKinds(t, result)
			for _, requiredKind := range testCase.requiredKinds {
				if !kinds[requiredKind] {
					t.Fatalf("missing %s version from partial outcome: %#v", requiredKind, result.Output["versions"])
				}
			}
		})
	}
}

// outputVersionKinds extracts only safe kind tokens from a normalized test envelope.
func outputVersionKinds(t *testing.T, result harness.ToolResult) map[string]bool {
	t.Helper()
	kinds := map[string]bool{}
	for _, rawVersion := range result.Output["versions"].([]any) {
		versionValue := rawVersion.(map[string]any)
		kinds[versionValue["kind"].(string)] = true
	}
	return kinds
}

// TestOperationalVersionFinalizationIsCanonical preserves safe versions across Pod and Event failures.
func TestOperationalVersionFinalizationIsCanonical(t *testing.T) {
	podA := Pod{UID: "pod-a", OwnerUID: "deployment-uid", ResourceVersion: "p-a"}
	podB := Pod{UID: "pod-b", OwnerUID: "deployment-uid", ResourceVersion: "p-b"}
	incompletePod := Pod{OwnerUID: "deployment-uid", ResourceVersion: "p-invalid"}
	firstPodTool, _ := validTool(t, &fakeReader{podPages: []PodPage{{Items: []Pod{podA, podB, incompletePod}, ResourceVersion: "pods-v1"}}})
	secondPodTool, _ := validTool(t, &fakeReader{podPages: []PodPage{{Items: []Pod{podB, podA, incompletePod}, ResourceVersion: "pods-v1"}}})
	firstPod, firstPodErr := execute(t, firstPodTool, CheckPodRestarts, "pods")
	secondPod, secondPodErr := execute(t, secondPodTool, CheckPodRestarts, "pods")
	if firstPodErr == nil || secondPodErr == nil || firstPod.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) || mustJSON(firstPod.Output) != mustJSON(secondPod.Output) || len(firstPod.Output["versions"].([]any)) != 5 {
		t.Fatalf("Pod insufficient versions were not canonical: first=%s second=%s", mustJSON(firstPod.Output), mustJSON(secondPod.Output))
	}

	eventA := Event{UID: "event-a", InvolvedUID: "deployment-uid", ResourceVersion: "e-a", Count: 1}
	eventB := Event{UID: "event-b", InvolvedUID: "deployment-uid", ResourceVersion: "e-b", Count: 2}
	invalidEvent := Event{UID: "event-invalid", InvolvedUID: "deployment-uid", ResourceVersion: "e-invalid", Count: -1}
	firstEventTool, _ := validTool(t, &fakeReader{eventPages: []EventPage{{Items: []Event{eventA, eventB, eventA, invalidEvent}, ResourceVersion: "events-v1"}}})
	secondEventTool, _ := validTool(t, &fakeReader{eventPages: []EventPage{{Items: []Event{eventB, eventA, invalidEvent}, ResourceVersion: "events-v1"}}})
	firstEvent, firstEventErr := execute(t, firstEventTool, CheckEvents, "events")
	secondEvent, secondEventErr := execute(t, secondEventTool, CheckEvents, "events")
	if firstEventErr == nil || secondEventErr == nil || firstEvent.Output["outcome"].(map[string]any)["category"] != string(OutcomeIntegrity) || mustJSON(firstEvent.Output) != mustJSON(secondEvent.Output) || len(firstEvent.Output["versions"].([]any)) != 5 {
		t.Fatalf("Event integrity versions were not canonical: first=%s second=%s", mustJSON(firstEvent.Output), mustJSON(secondEvent.Output))
	}
}
