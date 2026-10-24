package backend

import (
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
)

// GrantPermission grants, denies, or persistently grants a permission
// request. The returned bool reports whether this call resolved the
// pending request (true) or found it already resolved by a previous
// caller (false). A false return is not an error.
func (b *Backend) GrantPermission(workspaceID string, req proto.PermissionGrant) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}

	perm := permission.PermissionRequest{
		ID:          req.Permission.ID,
		SessionID:   req.Permission.SessionID,
		ToolCallID:  req.Permission.ToolCallID,
		ToolName:    req.Permission.ToolName,
		Description: req.Permission.Description,
		Action:      req.Permission.Action,
		Params:      req.Permission.Params,
		Path:        req.Permission.Path,
	}

	switch req.Action {
	case proto.PermissionAllow:
		return ws.Permissions.Grant(perm), nil
	case proto.PermissionAllowForSession:
		return ws.Permissions.GrantPersistent(perm), nil
	case proto.PermissionDeny:
		return ws.Permissions.Deny(perm), nil
	default:
		return false, ErrInvalidPermissionAction
	}
}

// SetPermissionsSkip sets whether permission prompts are skipped.
func (b *Backend) SetPermissionsSkip(workspaceID string, skip bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.Permissions.SetSkipRequests(skip)
	return nil
}

// GetPermissionsSkip returns whether permission prompts are skipped.
func (b *Backend) GetPermissionsSkip(workspaceID string) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}

	return ws.Permissions.SkipRequests(), nil
}

// SetPermissionMode switches the workspace's permission mode. The change is
// a user action, so leaving plan mode counts as explicit approval; bypass
// still follows the deployment's configuration.
func (b *Backend) SetPermissionMode(workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	modeService, ok := ws.Permissions.(permission.ModeService)
	if !ok {
		return ErrPermissionModeUnavailable
	}
	mode, err := permission.ParseMode(name)
	if err != nil {
		return err
	}
	options := modeService.ModeOptions()
	if err := permission.ValidateTransition(modeService.Mode(), mode, permission.ModeOptions{
		BypassAllowed:    options.BypassAllowed,
		PlanExitApproved: true,
	}); err != nil {
		return err
	}
	modeService.SetMode(mode)
	return nil
}

// GetPermissionMode returns the workspace's current permission mode.
func (b *Backend) GetPermissionMode(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	modeService, ok := ws.Permissions.(permission.ModeService)
	if !ok {
		return "", ErrPermissionModeUnavailable
	}
	return string(modeService.Mode()), nil
}
