package model

import (
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// permissionModeController is the optional workspace capability for reading
// and switching the runtime permission mode. Workspaces that cannot enforce
// modes simply omit it, so the UI falls back to a message instead of a
// silent no-op.
type permissionModeController interface {
	PermissionMode() string
	PermissionSetMode(mode string) error
}

// cyclePermissionMode advances to the next permission mode and persists the
// choice. The runtime switch happens first: a failed persistence still
// leaves the session in the mode the user picked.
func (m *UI) cyclePermissionMode() tea.Cmd {
	controller, ok := m.com.Workspace.(permissionModeController)
	if !ok {
		return util.ReportWarn("Permission modes are unavailable in this workspace.")
	}

	bypassAllowed := true
	if cfg := m.com.Config(); cfg != nil && cfg.Permissions != nil {
		bypassAllowed = !cfg.Permissions.DisableBypassPermissions
	}

	current, err := permission.ParseMode(controller.PermissionMode())
	if err != nil {
		current = permission.ModeDefault
	}
	next := current.Next(bypassAllowed)
	if err := controller.PermissionSetMode(string(next)); err != nil {
		return util.ReportError(err)
	}

	info := util.CmdHandler(util.NewInfoMsg("Permission mode: " + next.Label()))
	if cfgErr := m.com.Workspace.SetConfigField(config.ScopeGlobal, "permissions.mode", string(next)); cfgErr != nil {
		return tea.Batch(info, util.ReportError(cfgErr))
	}
	return info
}
