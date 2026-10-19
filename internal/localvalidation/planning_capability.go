package localvalidation

import (
	"encoding/json"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/specification"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// BuildHTTPPlanningCapability derives the model descriptor from the same executable HTTP tool configuration used by Runner.
func BuildHTTPPlanningCapability(spec specification.Specification, manifest planmanifest.Manifest, target string, deps RuntimeDependencies) (agentplanning.CapabilityDescriptor, error) {
	spec.Target.BaseURL = target
	h := manifest.HTTP
	tool, err := httptool.New(httptool.Config{Target: spec.Target, AllowedMethods: h.AllowedMethods, AllowedRequestHeaders: h.AllowedRequestHeaders, AllowedResponseHeaders: h.AllowedResponseHeaders, FollowRedirectsForMethods: h.FollowRedirectsForMethods, MaxRedirects: h.MaxRedirects, Timeout: h.Timeout(), MaxURLBytes: h.MaxURLBytes, MaxRequestHeaderBytes: h.MaxRequestHeaderBytes, MaxRequestBodyBytes: h.MaxRequestBodyBytes, MaxResponseHeaderBytes: h.MaxResponseHeaderBytes, MaxResponseBodyBytes: h.MaxResponseBodyBytes, Resolver: deps.Resolver, Dialer: deps.Dialer, Clock: deps.Clock, WithTimeout: deps.WithTimeout})
	if err != nil {
		return agentplanning.CapabilityDescriptor{}, err
	}
	specification := tool.Spec()
	return agentplanning.CapabilityDescriptor{Name: specification.Name, Class: "http", Description: "bounded HTTP request", Effects: specification.Effects, Idempotent: specification.Idempotent, Validate: func(input map[string]planning.InputValue) error {
		converted := make(map[string]any, len(input))
		for key, value := range input {
			converted[key] = planningInputValue(value)
		}
		return specification.Validate(converted)
	}}, nil
}

// planningInputValue converts a validated planning union into the executable tool validator's JSON-shaped input.
func planningInputValue(value planning.InputValue) any {
	if value.Literal != nil {
		return value.Literal
	}
	if value.Reference != nil {
		raw, _ := json.Marshal(value.Reference)
		var out any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	if value.Object != nil {
		out := make(map[string]any, len(value.Object))
		for key, child := range value.Object {
			out[key] = planningInputValue(child)
		}
		return out
	}
	out := make([]any, len(value.Array))
	for index, child := range value.Array {
		out[index] = planningInputValue(child)
	}
	return out
}
