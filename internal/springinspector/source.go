package springinspector

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// SnapshotRequest is the complete bounded read authority supplied to a passive repository capability.
type SnapshotRequest struct {
	Root       string
	SourceSets []SourceSet
	Limits     Limits
}

// RepositoryFS is the only capability permitted to materialize repository bytes for this package.
type RepositoryFS interface {
	ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error)
}

// Clock supplies one trusted instant for sealing the cross-phase inspection deadline.
type Clock interface{ Now() time.Time }

// SystemClock supplies wall time only at the composition boundary.
type SystemClock struct{}

// Now returns the production instant used to seal a deadline.
func (SystemClock) Now() time.Time { return time.Now() }

// FreezeConfiguredRepository validates all authority before invoking the passive repository capability.
func FreezeConfiguredRepository(ctx context.Context, repository RepositoryFS, root string, config Configuration) (Input, error) {
	return FreezeConfiguredRepositoryWithClock(ctx, repository, root, config, SystemClock{})
}

// FreezeConfiguredRepositoryWithClock seals bytes and one shared absolute deadline without host defaults.
func FreezeConfiguredRepositoryWithClock(ctx context.Context, repository RepositoryFS, root string, config Configuration, clock Clock) (Input, error) {
	if err := validateConfiguration(config); err != nil {
		return Input{}, err
	}
	if repository == nil || root == "" || clock == nil || config.RootIdentity == "" {
		return Input{}, typedError(CategoryInvalidInput, "filesystem", nil)
	}
	now := clock.Now()
	bounded, cancel := context.WithDeadline(ctx, now.Add(config.Limits.MaxDuration))
	defer cancel()
	sets := make([]SourceSet, 0, len(config.SourceSets))
	for _, set := range config.SourceSets {
		if !set.Generated || config.Generated == GeneratedInclude {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return Input{}, typedError(CategoryInsufficientContext, "filesystem.source_sets", nil)
	}
	files, err := repository.ReadSnapshot(bounded, SnapshotRequest{Root: root, SourceSets: sets, Limits: config.Limits})
	if err != nil {
		if bounded.Err() != nil {
			return Input{}, typedError(CategoryCancelled, "filesystem", bounded.Err())
		}
		return Input{}, typedError(CategoryInvalidInput, "filesystem", nil)
	}
	activeRoots := make([]string, len(sets))
	for index, set := range sets {
		activeRoots[index] = set.Root
	}
	for name := range files {
		clean, ok := safeRelativePath(name)
		if !ok || clean != name || !withinRoots(name, activeRoots) {
			return Input{}, typedError(CategoryInvalidInput, "filesystem.scope", nil)
		}
	}
	snapshot, err := NewSnapshot(config.RepositoryID, config.Revision, files)
	if err != nil {
		return Input{}, err
	}
	deadline, _ := bounded.Deadline()
	return newInput(snapshot, config, deadline)
}

// DerivedSource adapts the inspector to the source-neutral trusted registry.
type DerivedSource struct{ Inspector Inspector }

// Load rejects descriptor drift before parsing and revalidates the immutable result through the registry.
func (d DerivedSource) Load(ctx context.Context, request contractcatalog.SourceRequest) (contractcatalog.SourceSnapshot, error) {
	input, ok := request.Input().(Input)
	if !ok || request.Kind() != SourceKind || request.Class() != contractcatalog.ClassDerived || request.AdapterVersion() != AdapterVersion || request.ExpectedDigest() != input.Digest() || request.Confidence() != input.config.Confidence || request.Completeness() != input.config.Completeness || !reflect.DeepEqual(request.Projection(), input.config.Projection) {
		return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryInvalidSource, Location: "spring_inspector"}
	}
	result, err := d.Inspector.Inspect(ctx, input, request.ID())
	if err != nil {
		var inspection *Error
		if errors.As(err, &inspection) {
			category := contractcatalog.CategoryInvalidSource
			switch inspection.Category {
			case CategoryInsufficientContext:
				category = contractcatalog.CategoryInsufficient
			case CategoryLimitExceeded:
				category = contractcatalog.CategoryLimitExceeded
			case CategoryCancelled:
				category = contractcatalog.CategoryCancelled
			}
			return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: category, Location: "spring_inspector", Cause: inspection.Cause}
		}
		return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryInternal, Location: "spring_inspector"}
	}
	return result.Snapshot, nil
}

// SourceRequest constructs the exact derived-source descriptor for one sealed input.
func SourceRequest(sourceID string, input Input, limits contractcatalog.Limits, projection contractcatalog.ProjectionPolicy) contractcatalog.SourceRequest {
	return contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: SourceKind, ID: sourceID, Class: contractcatalog.ClassDerived, Confidence: input.config.Confidence, Completeness: input.config.Completeness, ExpectedDigest: input.Digest(), AdapterVersion: AdapterVersion, Input: input, Limits: limits, Projection: projection})
}
