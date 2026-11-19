package projectconfig

import "slices"

// Action is one closed application-service capability.
type Action string

const (
	// ActionValidate permits non-mutating structural and semantic validation.
	ActionValidate Action = "validate"
	// ActionPublish permits atomically publishing and activating a new version.
	ActionPublish Action = "publish"
	// ActionRollback permits a revalidated historical version activation.
	ActionRollback Action = "rollback"
	// ActionRead permits scoped active, version and history queries.
	ActionRead Action = "read"
	// ActionExport permits returning canonical non-secret configuration bytes.
	ActionExport Action = "export"
	// ActionAdmit permits freezing configuration authority into a run binding.
	ActionAdmit Action = "admit"
)

// ConfigurationAuthority is an opaque composition-issued project capability; organization membership and RBAC remain the feature 37 composition boundary.
type ConfigurationAuthority struct {
	actor   string
	key     ProjectKey
	actions []Action
}

// IssueAuthority validates a trusted composition decision and returns an immutable capability.
func IssueAuthority(actor string, key ProjectKey, actions ...Action) (ConfigurationAuthority, error) {
	if !safeActor(actor) || key.Validate() != nil || len(actions) == 0 {
		return ConfigurationAuthority{}, &Error{Category: CategoryUnauthorized, Operation: "authority.issue"}
	}
	allowed := map[Action]bool{ActionValidate: true, ActionPublish: true, ActionRollback: true, ActionRead: true, ActionExport: true, ActionAdmit: true}
	copyActions := append([]Action(nil), actions...)
	for _, action := range copyActions {
		if !allowed[action] {
			return ConfigurationAuthority{}, &Error{Category: CategoryUnauthorized, Operation: "authority.issue"}
		}
	}
	slices.Sort(copyActions)
	copyActions = slices.Compact(copyActions)
	return ConfigurationAuthority{actor: actor, key: key, actions: copyActions}, nil
}

// authorize proves exact action and project scope before any collaborator call.
func (authority ConfigurationAuthority) authorize(action Action, key ProjectKey) error {
	if !safeActor(authority.actor) || authority.key != key || !slices.Contains(authority.actions, action) {
		return &Error{Category: CategoryUnauthorized, Operation: string(action)}
	}
	return nil
}

// Actor returns the safe audit identity after successful authorization.
func (authority ConfigurationAuthority) Actor() string { return authority.actor }
