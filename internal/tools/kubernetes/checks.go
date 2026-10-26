package kubernetes

import (
	"context"
	"errors"
	"sort"
)

// observeDeployment projects finite rollout counters without deciding whether they satisfy a feature.
func (t *Tool) observeDeployment(ctx context.Context, resource Resource, result *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := t.reader.GetDeployment(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if value.UID != resource.UID || resource.OwnerUID != "" && value.OwnerUID != resource.OwnerUID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias, string(resource.Kind), value.UID, value.ResourceVersion))
	if !validDeploymentCounters(value) {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Observation = map[string]any{"generation": value.Generation, "observed_generation": value.ObservedGeneration, "desired_replicas": value.Desired, "updated_replicas": value.Updated, "available_replicas": value.Available, "ready_replicas": value.Ready, "conditions": t.conditions(value.Conditions)}
	if err := ctx.Err(); err != nil {
		return err
	}
	anchor, err := t.reader.GetDeployment(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if anchor.UID != value.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias+":final", string(resource.Kind), anchor.UID, anchor.ResourceVersion))
	if anchor.ResourceVersion != value.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
	}
	return nil
}

// observePods aggregates only exact-owner Pods and marks every incomplete collection inconclusive.
func (t *Tool) observePods(ctx context.Context, resource Resource, result *Result) error {
	owner := t.authority.resources[resource.OwnerAlias]
	if err := ctx.Err(); err != nil {
		return err
	}
	initialOwner, err := t.reader.GetDeployment(ctx, t.authority.namespace, owner.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if initialOwner.UID != owner.UID || initialOwner.UID != resource.OwnerUID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias+":owner", string(owner.Kind), initialOwner.UID, initialOwner.ResourceVersion))
	items, versions, truncated, observed, err := t.pods(ctx, resource)
	result.Versions = append(result.Versions, versions...)
	if err != nil {
		return err
	}
	if truncated {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Truncated: true}
		result.Truncations = append(result.Truncations, Truncation{Section: "pods", Observed: observed, Retained: len(items)})
		result.Observation = nil
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	finalOwner, err := t.reader.GetDeployment(ctx, t.authority.namespace, owner.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if finalOwner.UID != initialOwner.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias+":owner-final", string(owner.Kind), finalOwner.UID, finalOwner.ResourceVersion))
	if finalOwner.ResourceVersion != initialOwner.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
		return nil
	}
	if len(items) == 0 {
		result.Outcome = Outcome{Category: OutcomeAbsent, Complete: true}
		result.Observation = nil
		return nil
	}
	projected := make([]map[string]any, 0, len(items))
	for _, pod := range items {
		containers := make([]map[string]any, 0, len(pod.Containers))
		seenRoles := map[string]bool{}
		for _, container := range pod.Containers {
			if container.Role == "" || seenRoles[container.Role] {
				return &ReaderError{Category: OutcomeIntegrity}
			}
			if container.Restarts < 0 {
				return &ReaderError{Category: OutcomeIntegrity}
			}
			seenRoles[container.Role] = true
			if containsString(resource.ContainerRoles, container.Role) {
				containers = append(containers, map[string]any{"role": container.Role, "ready": container.Ready, "restart_count": container.Restarts})
			}
		}
		sort.Slice(containers, func(left, right int) bool {
			return containers[left]["role"].(string) < containers[right]["role"].(string)
		})
		projected = append(projected, map[string]any{"pod_alias": digest(pod.UID)[:16], "phase": safeEnum(pod.Phase, "Pending", "Running", "Succeeded", "Failed", "Unknown"), "ready": pod.Ready, "terminating": pod.Terminating, "containers": containers})
	}
	sort.Slice(projected, func(left, right int) bool {
		return projected[left]["pod_alias"].(string) < projected[right]["pod_alias"].(string)
	})
	result.Observation = map[string]any{"pods": projected, "count": len(projected)}
	return nil
}

// pods follows bounded pagination while rejecting cycles and objects outside the sealed owner.
func (t *Tool) pods(ctx context.Context, resource Resource) ([]Pod, []Version, bool, int, error) {
	items := []Pod{}
	versions := []Version{}
	seenTokens := map[string]bool{}
	seenUIDs := map[string]bool{}
	token := ""
	listVersion := ""
	observed := 0
	for pageNumber := 0; pageNumber < t.maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return items, versions, false, observed, err
		}
		page, err := t.reader.ListOwnedPodsPage(ctx, t.authority.namespace, resource.Selector, PageRequest{Limit: t.pageSize, Continue: token, ResourceVersion: listVersion})
		if err != nil {
			return items, versions, false, observed, err
		}
		if listVersion != "" && page.ResourceVersion != listVersion || page.ResourceVersion == "" && page.Continue != "" || len(page.Items) == 0 && page.Continue != "" {
			return items, versions, true, observed, nil
		}
		if listVersion == "" {
			listVersion = page.ResourceVersion
			versions = append(versions, version(resource.Alias+":list", "PodList", resource.OwnerUID, listVersion))
		}
		for _, item := range page.Items {
			observed++
			if item.UID == "" || item.OwnerUID == "" {
				return items, versions, false, observed, &ReaderError{Category: OutcomeInsufficient}
			}
			if item.OwnerUID != resource.OwnerUID {
				continue
			}
			if seenUIDs[item.UID] {
				return items, versions, false, observed, &ReaderError{Category: OutcomeIntegrity}
			}
			seenUIDs[item.UID] = true
			if len(items) >= t.maxItems {
				return items, versions, true, observed, nil
			}
			items = append(items, item)
			versions = append(versions, version(resource.Alias+":"+digest(item.UID)[:16], "Pod", item.UID, item.ResourceVersion))
		}
		if page.Continue == "" {
			return items, versions, false, observed, nil
		}
		if page.Continue == token || seenTokens[page.Continue] {
			return items, versions, true, observed, nil
		}
		seenTokens[page.Continue], token = true, page.Continue
	}
	return items, versions, token != "", observed, nil
}

// observeJob projects counters and allowlisted terminal conditions for one exact Job.
func (t *Tool) observeJob(ctx context.Context, resource Resource, result *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := t.reader.GetJob(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if value.UID != resource.UID || resource.OwnerUID != "" && value.OwnerUID != resource.OwnerUID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias, string(resource.Kind), value.UID, value.ResourceVersion))
	if !validJobCounters(value) {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Observation = map[string]any{"generation": value.Generation, "completions": value.Completions, "active": value.Active, "succeeded": value.Succeeded, "failed": value.Failed, "conditions": t.conditions(value.Conditions)}
	if err := ctx.Err(); err != nil {
		return err
	}
	anchor, err := t.reader.GetJob(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if anchor.UID != value.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias+":final", string(resource.Kind), anchor.UID, anchor.ResourceVersion))
	if anchor.ResourceVersion != value.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
	}
	return nil
}

// observeService aggregates address-free EndpointSlice readiness for one exact Service.
func (t *Tool) observeService(ctx context.Context, resource Resource, result *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	service, err := t.reader.GetService(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.UID != resource.UID || resource.OwnerUID != "" && service.OwnerUID != resource.OwnerUID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias, string(resource.Kind), service.UID, service.ResourceVersion))
	if !servicePortsMatch(service.Ports, resource.Ports) {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	ready, notReady, versions, truncated, observed, err := t.endpoints(ctx, resource, service.UID)
	result.Versions = append(result.Versions, versions...)
	if err != nil {
		return err
	}
	if truncated {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Truncated: true}
		result.Truncations = append(result.Truncations, Truncation{Section: "endpoint_slices", Observed: observed, Retained: len(versions)})
		result.Observation = nil
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ports := append([]ServicePort(nil), resource.Ports...)
	sort.Slice(ports, func(left, right int) bool {
		if ports[left].Port == ports[right].Port {
			return ports[left].Name < ports[right].Name
		}
		return ports[left].Port < ports[right].Port
	})
	projectedPorts := make([]map[string]any, 0, len(ports))
	for _, port := range ports {
		name := "other"
		if boundedID.MatchString(port.Name) {
			name = port.Name
		}
		projectedPorts = append(projectedPorts, map[string]any{"name": name, "protocol": safeEnum(port.Protocol, "TCP", "UDP", "SCTP"), "port": port.Port})
	}
	result.Observation = map[string]any{"service_type": safeEnum(service.Type, "ClusterIP", "ExternalName", "NodePort", "LoadBalancer"), "ports": projectedPorts, "ready_endpoints": ready, "not_ready_endpoints": notReady}
	if err := ctx.Err(); err != nil {
		return err
	}
	anchor, err := t.reader.GetService(ctx, t.authority.namespace, resource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if anchor.UID != service.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(resource.Alias+":final", string(resource.Kind), anchor.UID, anchor.ResourceVersion))
	if anchor.ResourceVersion != service.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
	}
	return nil
}

// endpoints follows bounded pagination and accepts only slices owned by the catalogued Service.
func (t *Tool) endpoints(ctx context.Context, resource Resource, serviceUID string) (int, int, []Version, bool, int, error) {
	ready, notReady, observed, retained := 0, 0, 0, 0
	versions := []Version{}
	seenTokens := map[string]bool{}
	seenUIDs := map[string]bool{}
	token := ""
	listVersion := ""
	for pageNumber := 0; pageNumber < t.maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return ready, notReady, versions, false, observed, err
		}
		page, err := t.reader.ListServiceEndpointSlicesPage(ctx, t.authority.namespace, resource.Name, PageRequest{Limit: t.pageSize, Continue: token, ResourceVersion: listVersion})
		if err != nil {
			return ready, notReady, versions, false, observed, err
		}
		if err := ctx.Err(); err != nil {
			return ready, notReady, versions, false, observed, err
		}
		if listVersion != "" && page.ResourceVersion != listVersion || page.ResourceVersion == "" && page.Continue != "" || len(page.Items) == 0 && page.Continue != "" {
			return ready, notReady, versions, true, observed, nil
		}
		if listVersion == "" {
			listVersion = page.ResourceVersion
			versions = append(versions, version(resource.Alias+":endpoint-list", "EndpointSliceList", serviceUID, listVersion))
		}
		for _, item := range page.Items {
			observed++
			if item.UID == "" || item.ServiceUID == "" || item.OwnerUID == "" {
				return ready, notReady, versions, false, observed, &ReaderError{Category: OutcomeIntegrity}
			}
			if item.ServiceUID != serviceUID || item.OwnerUID != serviceUID {
				continue
			}
			if seenUIDs[item.UID] {
				return ready, notReady, versions, false, observed, &ReaderError{Category: OutcomeIntegrity}
			}
			seenUIDs[item.UID] = true
			if retained >= t.maxItems {
				return ready, notReady, versions, true, observed, nil
			}
			versions = append(versions, version(resource.Alias+":slice:"+digest(item.UID)[:16], "EndpointSlice", item.UID, item.ResourceVersion))
			if item.Ready < 0 || item.NotReady < 0 || integerAdditionOverflows(ready, item.Ready) || integerAdditionOverflows(notReady, item.NotReady) {
				return ready, notReady, versions, false, observed, &ReaderError{Category: OutcomeIntegrity}
			}
			ready += item.Ready
			notReady += item.NotReady
			retained++
		}
		if page.Continue == "" {
			return ready, notReady, versions, false, observed, nil
		}
		if page.Continue == token || seenTokens[page.Continue] {
			return ready, notReady, versions, true, observed, nil
		}
		seenTokens[page.Continue], token = true, page.Continue
	}
	return ready, notReady, versions, token != "", observed, nil
}

// observeEvents retains only catalogued involved identities and allowlisted status vocabulary.
func (t *Tool) observeEvents(ctx context.Context, resource Resource, result *Result) error {
	items := []Event{}
	seenTokens := map[string]bool{}
	seenEvents := map[string]bool{}
	token := ""
	listVersion := ""
	observed := 0
	truncated := false
	for pageNumber := 0; pageNumber < t.maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := t.reader.ListScopedEventsPage(ctx, t.authority.namespace, []string{resource.UID}, PageRequest{Limit: t.pageSize, Continue: token, ResourceVersion: listVersion})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if listVersion != "" && page.ResourceVersion != listVersion || page.ResourceVersion == "" && page.Continue != "" || len(page.Items) == 0 && page.Continue != "" {
			truncated = true
			break
		}
		if listVersion == "" {
			listVersion = page.ResourceVersion
			result.Versions = append(result.Versions, version(resource.Alias+":list", "EventList", resource.UID, listVersion))
		}
		for _, item := range page.Items {
			observed++
			if item.InvolvedUID != resource.UID {
				continue
			}
			if item.UID == "" {
				return &ReaderError{Category: OutcomeIntegrity}
			}
			result.Versions = append(result.Versions, version(resource.Alias+":event:"+digest(item.UID)[:16], "Event", item.UID, item.ResourceVersion))
			if item.Count < 0 || !item.FirstObserved.IsZero() && !item.LastObserved.IsZero() && item.FirstObserved.After(item.LastObserved) {
				return &ReaderError{Category: OutcomeIntegrity}
			}
			if seenEvents[item.UID] {
				continue
			}
			seenEvents[item.UID] = true
			if len(items) >= t.maxEvents {
				truncated = true
				break
			}
			items = append(items, item)
		}
		if truncated || page.Continue == "" {
			break
		}
		if page.Continue == token || seenTokens[page.Continue] {
			truncated = true
			break
		}
		seenTokens[page.Continue], token = true, page.Continue
		if pageNumber == t.maxPages-1 {
			truncated = true
		}
	}
	if truncated {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Truncated: true}
		result.Truncations = append(result.Truncations, Truncation{Section: "events", Observed: observed, Retained: len(items)})
		result.Observation = nil
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	projected := make([]map[string]any, 0, len(items))
	for _, item := range items {
		reason := t.reason(item.Reason)
		projected = append(projected, map[string]any{"resource_alias": resource.Alias, "type": safeEnum(item.Type, "Normal", "Warning"), "reason": reason, "count": item.Count, "first_observed": item.FirstObserved.UTC().Format("2006-01-02T15:04:05Z"), "last_observed": item.LastObserved.UTC().Format("2006-01-02T15:04:05Z")})
	}
	sort.Slice(projected, func(left, right int) bool {
		leftKeys := []string{projected[left]["last_observed"].(string), projected[left]["reason"].(string), projected[left]["type"].(string), projected[left]["first_observed"].(string)}
		rightKeys := []string{projected[right]["last_observed"].(string), projected[right]["reason"].(string), projected[right]["type"].(string), projected[right]["first_observed"].(string)}
		for index := range leftKeys {
			if leftKeys[index] != rightKeys[index] {
				return leftKeys[index] < rightKeys[index]
			}
		}
		return projected[left]["count"].(int32) < projected[right]["count"].(int32)
	})
	result.Observation = map[string]any{"events": projected, "count": len(projected)}
	return nil
}

// observeConnectivity compares only the sealed declared relationship and never performs an active probe.
func (t *Tool) observeConnectivity(ctx context.Context, relationship Relationship, result *Result) error {
	destination := t.authority.resources[relationship.DestinationAlias]
	policyResource := t.authority.resources[relationship.PolicyAlias]
	if err := ctx.Err(); err != nil {
		return err
	}
	service, err := t.reader.GetService(ctx, t.authority.namespace, destination.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.UID != destination.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(destination.Alias, string(destination.Kind), service.UID, service.ResourceVersion))
	if !servicePortsMatch(service.Ports, destination.Ports) {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	ready, notReady, versions, truncated, observed, err := t.endpoints(ctx, destination, service.UID)
	result.Versions = append(result.Versions, versions...)
	if err != nil {
		return err
	}
	if truncated {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Truncated: true}
		result.Truncations = append(result.Truncations, Truncation{Section: "endpoint_slices", Observed: observed, Retained: len(versions)})
		return nil
	}
	if ready == 0 && notReady == 0 || len(versions) <= 1 {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	policy, err := t.reader.GetNetworkPolicy(ctx, t.authority.namespace, policyResource.Name)
	if err != nil {
		var readerErr *ReaderError
		if errors.As(err, &readerErr) && readerErr.Category == OutcomeAbsent {
			return &ReaderError{Category: OutcomeIntegrity}
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Versions = append(result.Versions, version(policyResource.Alias, string(policyResource.Kind), policy.UID, policy.ResourceVersion))
	if policy.UID != policyResource.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	if !policy.Declared || policy.Protocol != relationship.Protocol || policy.Port != relationship.Port || policy.SpecDigest != relationship.PolicyDigest {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Observation = map[string]any{"source_alias": relationship.SourceAlias, "destination_alias": relationship.DestinationAlias, "protocol": relationship.Protocol, "port": relationship.Port, "declared": true, "policy_present": true, "ready_endpoints": ready, "not_ready_endpoints": notReady}
	if err := ctx.Err(); err != nil {
		return err
	}
	serviceAnchor, err := t.reader.GetService(ctx, t.authority.namespace, destination.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if serviceAnchor.UID != service.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(destination.Alias+":final", string(destination.Kind), serviceAnchor.UID, serviceAnchor.ResourceVersion))
	policyAnchor, err := t.reader.GetNetworkPolicy(ctx, t.authority.namespace, policyResource.Name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if policyAnchor.UID != policy.UID {
		return &ReaderError{Category: OutcomeIntegrity}
	}
	result.Versions = append(result.Versions, version(policyResource.Alias+":final", string(policyResource.Kind), policyAnchor.UID, policyAnchor.ResourceVersion))
	if serviceAnchor.ResourceVersion != service.ResourceVersion || policyAnchor.ResourceVersion != policy.ResourceVersion || policyAnchor.SpecDigest != policy.SpecDigest {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
	}
	return nil
}

// conditions sorts condition flags and replaces non-allowlisted reasons with a stable token.
func (t *Tool) conditions(values []Condition) []map[string]any {
	output := make([]map[string]any, 0, len(values))
	for _, value := range values {
		output = append(output, map[string]any{"type": safeEnum(value.Type, "Available", "Progressing", "ReplicaFailure", "Complete", "Failed", "SuccessCriteriaMet", "FailureTarget"), "status": safeEnum(value.Status, "True", "False", "Unknown"), "reason": t.reason(value.Reason)})
	}
	sort.Slice(output, func(left, right int) bool {
		for _, field := range []string{"type", "status", "reason"} {
			if output[left][field].(string) != output[right][field].(string) {
				return output[left][field].(string) < output[right][field].(string)
			}
		}
		return false
	})
	deduplicated := output[:0]
	for _, item := range output {
		if len(deduplicated) == 0 || item["type"] != deduplicated[len(deduplicated)-1]["type"] || item["status"] != deduplicated[len(deduplicated)-1]["status"] || item["reason"] != deduplicated[len(deduplicated)-1]["reason"] {
			deduplicated = append(deduplicated, item)
		}
	}
	return deduplicated
}

// reason minimizes untrusted Kubernetes text to a configured allowlist.
func (t *Tool) reason(value string) string {
	if _, allowed := t.allowedReasons[value]; allowed {
		return value
	}
	return "other"
}

// containsString checks a closed container-role allowlist.
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// safeEnum replaces unexpected Kubernetes vocabulary rather than forwarding attacker-controlled strings.
func safeEnum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}

// servicePortsMatch compares the complete canonical API port set with composition-owned declarations.
func servicePortsMatch(observed, expected []ServicePort) bool {
	if len(observed) != len(expected) {
		return false
	}
	left, right := append([]ServicePort(nil), observed...), append([]ServicePort(nil), expected...)
	order := func(values []ServicePort) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Port != values[j].Port {
				return values[i].Port < values[j].Port
			}
			if values[i].Protocol != values[j].Protocol {
				return values[i].Protocol < values[j].Protocol
			}
			return values[i].Name < values[j].Name
		})
	}
	order(left)
	order(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// validDeploymentCounters rejects impossible rollout generations and negative replica observations.
func validDeploymentCounters(value Deployment) bool {
	return value.Generation >= 0 && value.ObservedGeneration >= 0 && value.ObservedGeneration <= value.Generation && value.Desired >= 0 && value.Updated >= 0 && value.Available >= 0 && value.Ready >= 0 && value.Available <= value.Ready
}

// validJobCounters rejects negative status values and completed work beyond an explicit completion target.
func validJobCounters(value Job) bool {
	if value.Generation < 0 || value.Completions < 0 || value.Active < 0 || value.Succeeded < 0 || value.Failed < 0 {
		return false
	}
	return value.Completions == 0 || value.Succeeded <= value.Completions && value.Active <= value.Completions-value.Succeeded
}

// integerAdditionOverflows guards address-free endpoint aggregation from wrapping the platform int.
func integerAdditionOverflows(total, increment int) bool {
	return increment > int(^uint(0)>>1)-total
}
