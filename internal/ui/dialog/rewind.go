package dialog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/shadowgit"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
)

const (
	// RewindID is the identifier for the rewind (time-travel) dialog.
	RewindID              = "rewind"
	rewindDialogMaxWidth  = 64
	rewindDialogMaxHeight = 22
	// rewindForkKey forks a new session from the selected checkpoint.
	rewindForkKey = "f"
)

// ActionRewindRestore asks the UI to restore the working tree to a
// checkpoint.
type ActionRewindRestore struct {
	SessionID    string
	CheckpointID string
}

// ActionRewindFork asks the UI to restore the working tree to a
// checkpoint and continue in a fresh session.
type ActionRewindFork struct {
	SessionID    string
	CheckpointID string
}

// rewindCheckpointsMsg is delivered to the dialog when the checkpoint
// list finished loading.
type rewindCheckpointsMsg struct {
	sessionID   string
	checkpoints []shadowgit.Checkpoint
	svc         shadowgit.Service
	err         error
}

// rewindDiffStatMsg carries a loaded diffstat for one checkpoint.
type rewindDiffStatMsg struct {
	checkpointID string
	stat         string
	err          error
}

// Rewind is the dialog that lists a session's shadow checkpoints and
// lets the user time-travel the working tree back to any of them.
type Rewind struct {
	com       *common.Common
	help      help.Model
	list      *list.FilterableList
	svc       shadowgit.Service
	sessionID string
	diffStats map[string]string
	loadErr   error
	loadingID string

	keyMap struct {
		Select   key.Binding
		Fork     key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Rewind)(nil)

// NewRewind creates a rewind dialog and the command that loads the
// session's checkpoints. The returned dialog shows a loading state
// until rewindCheckpointsMsg arrives.
func NewRewind(com *common.Common, sessionID string) (*Rewind, tea.Cmd) {
	r := &Rewind{
		com:       com,
		sessionID: sessionID,
		diffStats: make(map[string]string),
	}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	r.help = h

	r.list = list.NewFilterableList()
	r.list.Focus()

	r.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "restore"),
	)
	r.keyMap.Fork = key.NewBinding(
		key.WithKeys(rewindForkKey),
		key.WithHelp(rewindForkKey, "fork"),
	)
	r.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next"),
	)
	r.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous"),
	)
	r.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	r.keyMap.Close = CloseKey

	load := func() tea.Msg {
		msg := rewindCheckpointsMsg{sessionID: sessionID}
		svc, err := rewindService(com)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.svc = svc
		cps, err := svc.List(context.Background(), sessionID)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.checkpoints = cps
		return msg
	}
	return r, load
}

// rewindService resolves the shadow service from the workspace.
func rewindService(com *common.Common) (shadowgit.Service, error) {
	provider, ok := com.Workspace.(interface{ ShadowGit() shadowgit.Service })
	if !ok {
		return nil, fmt.Errorf("rewind is not available in this mode")
	}
	svc := provider.ShadowGit()
	if svc == nil || !svc.Enabled() {
		return nil, fmt.Errorf("rewind is not available outside a git repository")
	}
	return svc, nil
}

// ID implements Dialog.
func (r *Rewind) ID() string {
	return RewindID
}

// HandleMsg implements Dialog.
func (r *Rewind) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case rewindCheckpointsMsg:
		r.svc = msg.svc
		r.loadErr = msg.err
		r.setItems(msg.checkpoints)
		if msg.err == nil && len(msg.checkpoints) > 0 {
			return r.previewDiffStat()
		}
	case rewindDiffStatMsg:
		if msg.err != nil {
			r.diffStats[msg.checkpointID] = "(diff unavailable)"
		} else {
			r.diffStats[msg.checkpointID] = msg.stat
		}
		r.loadingID = ""
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, r.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, r.keyMap.Previous):
			r.list.Focus()
			if r.list.IsSelectedFirst() {
				r.list.SelectLast()
				r.list.ScrollToBottom()
			} else {
				r.list.SelectPrev()
				r.list.ScrollToSelected()
			}
			return r.previewDiffStat()
		case key.Matches(msg, r.keyMap.Next):
			r.list.Focus()
			if r.list.IsSelectedLast() {
				r.list.SelectFirst()
				r.list.ScrollToTop()
			} else {
				r.list.SelectNext()
				r.list.ScrollToSelected()
			}
			return r.previewDiffStat()
		case key.Matches(msg, r.keyMap.Select):
			cp, ok := r.selectedCheckpoint()
			if !ok {
				break
			}
			return ActionRewindRestore{SessionID: r.sessionID, CheckpointID: cp.ID}
		case key.Matches(msg, r.keyMap.Fork):
			cp, ok := r.selectedCheckpoint()
			if !ok {
				break
			}
			return ActionRewindFork{SessionID: r.sessionID, CheckpointID: cp.ID}
		}
	}
	return nil
}

// selectedCheckpoint returns the checkpoint behind the selected list
// item.
func (r *Rewind) selectedCheckpoint() (shadowgit.Checkpoint, bool) {
	item := r.list.SelectedItem()
	ri, ok := item.(*rewindItem)
	if !ok || ri == nil {
		return shadowgit.Checkpoint{}, false
	}
	return ri.cp, true
}

// previewDiffStat loads the diffstat of the selected checkpoint off
// the UI goroutine. It returns the command that delivers the result.
func (r *Rewind) previewDiffStat() Action {
	cp, ok := r.selectedCheckpoint()
	if !ok || r.svc == nil {
		return nil
	}
	if _, done := r.diffStats[cp.ID]; done {
		return nil
	}
	if r.loadingID == cp.ID {
		return nil
	}
	r.loadingID = cp.ID
	cpID := cp.ID
	return ActionCmd{Cmd: func() tea.Msg {
		stat, err := r.svc.DiffStat(context.Background(), cpID)
		return rewindDiffStatMsg{checkpointID: cpID, stat: stat, err: err}
	}}
}

// Draw implements Dialog.
func (r *Rewind) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := r.com.Styles
	width := max(0, min(rewindDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(rewindDialogMaxHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()

	listHeight := max(0, height-heightOffset-3)
	r.list.SetSize(innerWidth, listHeight)

	rc := NewRenderContext(t, width)
	rc.Title = "Rewind"

	if r.loadErr != nil {
		rc.AddPart(r.loadErr.Error())
	} else if cp, ok := r.selectedCheckpoint(); ok {
		meta := cp.ToolName
		if meta == "" {
			meta = "snapshot"
		}
		when := cp.CreatedAt.Format("15:04:05")
		if cp.CreatedAt.IsZero() {
			when = "—"
		}
		files := "no file changes"
		if len(cp.Files) > 0 {
			files = fmt.Sprintf("%d files", len(cp.Files))
		}
		rc.AddPart(fmt.Sprintf("%s · %s · %s (%s)",
			cp.ID, when, meta, files))
		if stat, ok := r.diffStats[cp.ID]; ok {
			rc.AddPart(stat)
		} else {
			rc.AddPart("Loading diff…")
		}
	} else {
		rc.AddPart("Loading checkpoints…")
	}

	visibleCount := len(r.list.FilteredItems())
	if r.list.Height() >= visibleCount {
		r.list.ScrollToTop()
	} else {
		r.list.ScrollToSelected()
	}

	rc.AddPart(t.Dialog.List.Height(r.list.Height()).Render(r.list.Render()))
	rc.Help = renderDialogHelp(t, &r.help, r, innerWidth)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, nil)
	return nil
}

// ShortHelp implements help.KeyMap.
func (r *Rewind) ShortHelp() []key.Binding {
	return []key.Binding{
		r.keyMap.UpDown,
		r.keyMap.Select,
		r.keyMap.Fork,
		r.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (r *Rewind) FullHelp() [][]key.Binding {
	m := [][]key.Binding{}
	slice := []key.Binding{
		r.keyMap.Select,
		r.keyMap.Fork,
		r.keyMap.Next,
		r.keyMap.Previous,
		r.keyMap.Close,
	}
	for i := 0; i < len(slice); i += 4 {
		end := min(i+4, len(slice))
		m = append(m, slice[i:end])
	}
	return m
}

func (r *Rewind) setItems(checkpoints []shadowgit.Checkpoint) {
	items := make([]list.FilterableItem, 0, len(checkpoints))
	for _, cp := range checkpoints {
		items = append(items, &rewindItem{
			Versioned: list.NewVersioned(),
			cp:        cp,
			t:         r.com.Styles,
		})
	}
	r.list.SetItems(items...)
	r.list.SetSelected(len(items) - 1)
	r.list.ScrollToSelected()
}

// rewindItem renders one checkpoint row.
type rewindItem struct {
	*list.Versioned
	cp      shadowgit.Checkpoint
	t       *styles.Styles
	focused bool
	cache   map[int]string
	m       fuzzy.Match
}

// Filter returns the filter text for the item.
func (ri *rewindItem) Filter() string {
	return ri.cp.ToolName + " " + strings.Join(ri.cp.Files, " ")
}

// ID returns the checkpoint ID.
func (ri *rewindItem) ID() string {
	return ri.cp.ID
}

// Finished implements list.Item.
func (ri *rewindItem) Finished() bool {
	return true
}

// SetFocused sets the focus state of the item.
func (ri *rewindItem) SetFocused(focused bool) {
	if ri.focused == focused {
		return
	}
	ri.cache = nil
	ri.focused = focused
	if ri.Versioned != nil {
		ri.Bump()
	}
}

// SetMatch sets the fuzzy match for the item.
func (ri *rewindItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(ri.m, m) {
		return
	}
	ri.cache = nil
	ri.m = m
	if ri.Versioned != nil {
		ri.Bump()
	}
}

// Render returns the string representation of the item.
func (ri *rewindItem) Render(width int) string {
	st := ListItemStyles{
		ItemBlurred:     ri.t.Dialog.NormalItem,
		ItemFocused:     ri.t.Dialog.SelectedItem,
		InfoTextBlurred: ri.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: ri.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(st, ri.title(), ri.info(), ri.focused, width, ri.cache, &ri.m)
}

func (ri *rewindItem) title() string {
	when := ri.cp.CreatedAt.Format(time.TimeOnly)
	if ri.cp.CreatedAt.IsZero() {
		when = "—"
	}
	tool := ri.cp.ToolName
	if tool == "" {
		tool = "snapshot"
	}
	return fmt.Sprintf("%s  %s", when, tool)
}

func (ri *rewindItem) info() string {
	if ri.cp.Noop {
		return "no changes"
	}
	return fmt.Sprintf("%d files", len(ri.cp.Files))
}
