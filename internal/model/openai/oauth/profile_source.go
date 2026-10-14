package oauth

import "context"

// ProfileSource binds one opaque profile selection to the authenticated transport.
type ProfileSource struct {
	service   *Service
	profileID string
}

// NewProfileSource keeps the profile identity in composition instead of model contracts.
func NewProfileSource(service *Service, profileID string) (*ProfileSource, error) {
	if service == nil || profileID == "" {
		return nil, authError("compose profile source")
	}
	return &ProfileSource{service: service, profileID: profileID}, nil
}

// AccessToken resolves a current credential without exposing the selected profile upstream.
func (s *ProfileSource) AccessToken(ctx context.Context) (string, error) {
	return s.service.AccessToken(ctx, s.profileID)
}
