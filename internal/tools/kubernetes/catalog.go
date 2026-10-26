package kubernetes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// boundedID restricts aliases, object names, UIDs, and selectors supplied by trusted composition.
var boundedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/=,-]{0,255}$`)

// digestPattern accepts only canonical lowercase SHA-256 values.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SealCatalog validates, canonicalizes, detaches, and fingerprints inspection authority.
func SealCatalog(value Catalog) (Catalog, error) {
	value.Digest = ""
	if value.Version != CatalogVersion || !digestPattern.MatchString(value.TargetDigest) || !digestPattern.MatchString(value.SelectionDigest) || !boundedID.MatchString(value.ProviderVersion) || !boundedID.MatchString(value.Namespace) || !boundedID.MatchString(value.NamespaceUID) || len(value.Resources) == 0 {
		return Catalog{}, &ConfigError{Field: "catalog"}
	}
	resources := append([]Resource(nil), value.Resources...)
	aliases := make(map[string]Resource, len(resources))
	for index := range resources {
		resource := resources[index]
		resource.Checks = append([]CheckKind(nil), resource.Checks...)
		resource.ContainerRoles = append([]string(nil), resource.ContainerRoles...)
		resource.Ports = append([]ServicePort(nil), resource.Ports...)
		sort.Slice(resource.Checks, func(left, right int) bool { return resource.Checks[left] < resource.Checks[right] })
		sort.Strings(resource.ContainerRoles)
		sort.Slice(resource.Ports, func(left, right int) bool {
			if resource.Ports[left].Port != resource.Ports[right].Port {
				return resource.Ports[left].Port < resource.Ports[right].Port
			}
			if resource.Ports[left].Protocol != resource.Ports[right].Protocol {
				return resource.Ports[left].Protocol < resource.Ports[right].Protocol
			}
			return resource.Ports[left].Name < resource.Ports[right].Name
		})
		if !validResource(resource) {
			return Catalog{}, &ConfigError{Field: "catalog.resources"}
		}
		if _, duplicate := aliases[resource.Alias]; duplicate {
			return Catalog{}, &ConfigError{Field: "catalog.resources.alias"}
		}
		aliases[resource.Alias] = resource
		resources[index] = resource
	}
	for _, resource := range resources {
		if resource.Kind == KindPodSet {
			owner, exists := aliases[resource.OwnerAlias]
			if !exists || owner.Kind != KindDeployment || owner.UID != resource.OwnerUID {
				return Catalog{}, &ConfigError{Field: "catalog.resources.owner"}
			}
		}
	}
	sort.Slice(resources, func(left, right int) bool { return resources[left].Alias < resources[right].Alias })
	relationships := append([]Relationship(nil), value.Relationships...)
	relationAliases := make(map[string]struct{}, len(relationships))
	for _, relationship := range relationships {
		_, sourceOK := aliases[relationship.SourceAlias]
		destination, destinationOK := aliases[relationship.DestinationAlias]
		policy, policyOK := aliases[relationship.PolicyAlias]
		if !boundedID.MatchString(relationship.Alias) || !sourceOK || !destinationOK || destination.Kind != KindService || !policyOK || policy.Kind != KindNetworkPolicy || relationship.Protocol != strings.ToUpper(relationship.Protocol) || relationship.Protocol != "TCP" || relationship.Port < 1 || relationship.Port > 65535 || !digestPattern.MatchString(relationship.PolicyDigest) {
			return Catalog{}, &ConfigError{Field: "catalog.relationships"}
		}
		if !containsServicePort(destination.Ports, relationship.Protocol, relationship.Port) {
			return Catalog{}, &ConfigError{Field: "catalog.relationships.port"}
		}
		if _, duplicate := relationAliases[relationship.Alias]; duplicate {
			return Catalog{}, &ConfigError{Field: "catalog.relationships.alias"}
		}
		relationAliases[relationship.Alias] = struct{}{}
	}
	sort.Slice(relationships, func(left, right int) bool { return relationships[left].Alias < relationships[right].Alias })
	value.Resources, value.Relationships = resources, relationships
	value.Digest = digest(value)
	return value, nil
}

// containsServicePort proves a connectivity relation cannot introduce a port outside the sealed Service declaration.
func containsServicePort(values []ServicePort, protocol string, port int32) bool {
	for _, value := range values {
		if value.Protocol == protocol && value.Port == port {
			return true
		}
	}
	return false
}

// validResource rejects catalog entries that could widen authority or combine incompatible checks.
func validResource(value Resource) bool {
	if !boundedID.MatchString(value.Alias) || !boundedID.MatchString(value.Name) || !boundedID.MatchString(value.UID) || value.Kind != KindNetworkPolicy && len(value.Checks) == 0 || value.Kind == KindNetworkPolicy && len(value.Checks) != 0 {
		return false
	}
	if value.OwnerUID != "" && !boundedID.MatchString(value.OwnerUID) || value.Selector != "" && !boundedID.MatchString(value.Selector) {
		return false
	}
	seen := map[CheckKind]bool{}
	for _, check := range value.Checks {
		if seen[check] || !compatible(value.Kind, check) {
			return false
		}
		seen[check] = true
	}
	for _, role := range value.ContainerRoles {
		if !boundedID.MatchString(role) {
			return false
		}
	}
	if value.Kind == KindPodSet && (value.Selector == "" || value.OwnerUID == "" || !boundedID.MatchString(value.OwnerAlias)) {
		return false
	}
	if value.Kind == KindService {
		if len(value.Ports) == 0 {
			return false
		}
		seenPorts := map[string]bool{}
		for _, port := range value.Ports {
			key := port.Name + "\x00" + port.Protocol + "\x00" + strconv.FormatInt(int64(port.Port), 10)
			if !boundedID.MatchString(port.Name) || safeEnum(port.Protocol, "TCP", "UDP", "SCTP") == "other" || port.Port < 1 || port.Port > 65535 || seenPorts[key] {
				return false
			}
			seenPorts[key] = true
		}
		return true
	}
	return len(value.Ports) == 0
}

// compatible binds each resource kind to its closed set of observable checks.
func compatible(kind ResourceKind, check CheckKind) bool {
	switch kind {
	case KindDeployment:
		return check == CheckDeployment
	case KindPodSet:
		return check == CheckPodRestarts
	case KindJob:
		return check == CheckJob
	case KindService:
		return check == CheckService
	case KindEventScope:
		return check == CheckEvents
	case KindNetworkPolicy:
		return false
	default:
		return false
	}
}

// digest computes a canonical SHA-256 identity over map-free catalog and result projections.
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
