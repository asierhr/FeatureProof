package projectconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// ValidatePublishCommand enforces canonical scope and digests at every store boundary.
func ValidatePublishCommand(command PublishCommand) (ProjectKey, error) {
	key := ProjectKey{ProjectID: command.Document.Document.ProjectID, RepositoryID: command.Document.Document.RepositoryID}
	if key.Validate() != nil || !canonicalID.MatchString(command.OperationID) || !validDigest(command.CommandDigest) || !safeActor(command.Actor) || command.CreatedAt.IsZero() {
		return ProjectKey{}, invalid("store.publish", "command")
	}
	canonical, err := Canonicalize(command.Document.Document)
	if err != nil || canonical.Digest != command.Document.Digest || !bytes.Equal(canonical.Bytes, command.Document.Bytes) {
		return ProjectKey{}, &Error{Category: CategoryIntegrity, Operation: "store.publish.document"}
	}
	if err = validateEffective(key, command.Effective); err != nil {
		return ProjectKey{}, err
	}
	return key, nil
}

// ValidateRollbackCommand enforces historical activation identity before storage.
func ValidateRollbackCommand(command RollbackCommand) error {
	if command.ProjectKey.Validate() != nil || !canonicalID.MatchString(command.OperationID) || !validDigest(command.CommandDigest) || !safeActor(command.Actor) || command.Version == 0 || command.CreatedAt.IsZero() {
		return invalid("store.rollback", "command")
	}
	return validateEffective(command.ProjectKey, command.Effective)
}

// ValidateBindCommand rejects caller-controlled run identities or project scope.
func ValidateBindCommand(command BindCommand) error {
	if command.ProjectKey.Validate() != nil || !canonicalID.MatchString(command.RunID) {
		return invalid("store.bind", "command")
	}
	if command.Candidate != nil {
		if command.Candidate.Binding.RunID != command.RunID || command.Candidate.Binding.ProjectKey != command.ProjectKey {
			return invalid("store.bind", "candidate")
		}
		if err := ValidateRunBinding(command.Candidate.Binding); err != nil {
			return err
		}
	}
	return nil
}

// SealRunBinding computes the immutable identity of a complete non-secret run snapshot.
func SealRunBinding(binding RunConfigurationBinding) (RunConfigurationBinding, error) {
	binding = binding.Clone()
	binding.BindingDigest = ""
	encoded, err := json.Marshal(binding)
	if err != nil {
		return RunConfigurationBinding{}, invalid("store.binding", "canonical")
	}
	binding.BindingDigest = canonicalDigest(encoded)
	return binding, nil
}

// ValidateRunBinding detects adapter corruption independently of envelope checksums.
func ValidateRunBinding(binding RunConfigurationBinding) error {
	expected := binding.BindingDigest
	if !canonicalID.MatchString(binding.RunID) || binding.ProjectKey.Validate() != nil || binding.Version == 0 || binding.Version > MaxDurableCounter || binding.ActivationGeneration == 0 || binding.ActivationGeneration > MaxDurableCounter || binding.PolicyVersion == 0 || !validDigest(binding.VersionDigest) || !validDigest(binding.ActivationDigest) || !validDigest(binding.PolicyDigest) || !validDigest(binding.CatalogDigest) || !validDigest(binding.EffectiveDigest) || binding.Effective.ProjectKey != binding.ProjectKey || binding.Effective.DocumentDigest != binding.VersionDigest || binding.Effective.Digest != binding.EffectiveDigest || binding.Effective.PolicyVersion != binding.PolicyVersion || binding.Effective.PolicyDigest != binding.PolicyDigest || binding.Effective.CatalogDigest != binding.CatalogDigest || !validDigest(expected) {
		return &Error{Category: CategoryIntegrity, Operation: "store.binding"}
	}
	if err := validateEffective(binding.ProjectKey, binding.Effective); err != nil {
		return &Error{Category: CategoryIntegrity, Operation: "store.binding"}
	}
	sealed, err := SealRunBinding(binding)
	if err != nil || sealed.BindingDigest != expected {
		return &Error{Category: CategoryIntegrity, Operation: "store.binding"}
	}
	return nil
}

// ValidateDurableRunBinding verifies both the binding payload and its separately stored checksum.
func ValidateDurableRunBinding(binding RunConfigurationBinding, durableDigest string) error {
	if binding.BindingDigest != durableDigest || ValidateRunBinding(binding) != nil {
		return &Error{Category: CategoryIntegrity, Operation: "store.binding"}
	}
	return nil
}

// SealActivation canonicalizes the timestamp and seals every causal head field identically across stores.
func SealActivation(activation Activation) (Activation, error) {
	activation.CreatedAt = activation.CreatedAt.UTC().Truncate(time.Microsecond)
	activation.Digest = ""
	if activation.ProjectKey.Validate() != nil || activation.Generation == 0 || activation.Generation > MaxDurableCounter || activation.Version == 0 || activation.Version > MaxDurableCounter || activation.PreviousGeneration >= activation.Generation || !validDigest(activation.VersionDigest) || activation.Reason != "publish" && activation.Reason != "rollback" || !safeActor(activation.Actor) || activation.CreatedAt.IsZero() {
		return Activation{}, &Error{Category: CategoryIntegrity, Operation: "store.activation"}
	}
	encoded := []byte(fmt.Sprintf("%s:%s:%d:%d:%s:%d:%s:%s:%s", activation.ProjectKey.ProjectID, activation.ProjectKey.RepositoryID, activation.Generation, activation.Version, activation.VersionDigest, activation.PreviousGeneration, activation.Reason, activation.Actor, activation.CreatedAt.Format(time.RFC3339Nano)))
	activation.Digest = canonicalDigest(encoded)
	return activation, nil
}

// ValidateActivation detects corruption in a durable activation and its link to the immutable version.
func ValidateActivation(activation Activation, expectedVersionDigest string) error {
	expected := activation.Digest
	sealed, err := SealActivation(activation)
	if err != nil || !validDigest(expected) || sealed.Digest != expected || expectedVersionDigest != "" && activation.VersionDigest != expectedVersionDigest {
		return &Error{Category: CategoryIntegrity, Operation: "store.activation"}
	}
	return nil
}

// validateEffective recomputes the canonical digest so adapters never trust mutable bytes.
func validateEffective(key ProjectKey, effective EffectiveConfiguration) error {
	if effective.ProjectKey != key || !validDigest(effective.DocumentDigest) || effective.PolicyVersion == 0 || !validDigest(effective.PolicyDigest) || !validDigest(effective.CatalogDigest) || !validDigest(effective.Digest) {
		return &Error{Category: CategoryIntegrity, Operation: "store.effective"}
	}
	encoded, err := json.Marshal(effectiveCanonical{ProjectKey: key, DocumentDigest: effective.DocumentDigest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, References: effective.References, Descriptors: effective.Descriptors, Constraints: effective.Constraints})
	if err != nil || !bytes.Equal(encoded, effective.Bytes) || canonicalDigest(encoded) != effective.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "store.effective"}
	}
	return nil
}

// ValidateEffectiveSnapshot lets persistence adapters verify a decoded durable snapshot before returning it.
func ValidateEffectiveSnapshot(key ProjectKey, effective EffectiveConfiguration) error {
	return validateEffective(key, effective)
}

// ValidateDurableEffectiveSnapshot verifies a decoded snapshot against its separately stored checksum.
func ValidateDurableEffectiveSnapshot(key ProjectKey, effective EffectiveConfiguration, durableDigest string) error {
	if effective.Digest != durableDigest || validateEffective(key, effective) != nil {
		return &Error{Category: CategoryIntegrity, Operation: "store.effective"}
	}
	return nil
}
