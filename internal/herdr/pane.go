package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// SplitPaneID reads the new pane's id from `herdr pane split`'s answer.
func SplitPaneID(out []byte) (string, error) {
	var env struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return "", err
	}
	if env.Result.Pane.PaneID == "" {
		return "", errors.New("herdr pane split: answer has no result.pane.pane_id")
	}
	return env.Result.Pane.PaneID, nil
}

// PaneSize reads pane's size in cells from `herdr pane layout`'s answer. ok
// is false when the pane is missing or either side is not above zero.
func PaneSize(layout []byte, pane string) (w, h int, ok bool) {
	var env struct {
		Result struct {
			Layout struct {
				Panes []struct {
					PaneID string `json:"pane_id"`
					Rect   struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"rect"`
				} `json:"panes"`
			} `json:"layout"`
		} `json:"result"`
	}
	if json.Unmarshal(layout, &env) != nil {
		return 0, 0, false
	}
	for _, p := range env.Result.Layout.Panes {
		if p.PaneID == pane {
			w, h = p.Rect.Width, p.Rect.Height
			return w, h, w > 0 && h > 0
		}
	}
	return 0, 0, false
}

// SplitDirection splits a wide pane to the right and a narrow or tall one
// down. Cells are about twice as tall as wide, so "wide" is width > 2*height.
func SplitDirection(w, h int) string {
	if w > 2*h {
		return "right"
	}
	return "down"
}

// TabIsDefault reports whether a tab still has the label Herdr gives a tab
// nobody named: its 1-based position in its workspace, which Herdr renumbers
// when an earlier tab closes. tab is `herdr tab get`'s answer and list is
// `herdr tab list --workspace`'s answer for the tab's workspace. The tab's
// "number" is a global counter, not its label.
func TabIsDefault(tab, list []byte) (bool, error) {
	type tabInfo struct {
		ID    string `json:"tab_id"`
		Label string `json:"label"`
	}
	var got struct {
		Result struct {
			Tab *tabInfo `json:"tab"`
		} `json:"result"`
	}
	if err := json.Unmarshal(tab, &got); err != nil {
		return false, err
	}
	if got.Result.Tab == nil {
		return false, errors.New("herdr tab get: answer has no result.tab")
	}
	var all struct {
		Result struct {
			Tabs []tabInfo `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list, &all); err != nil {
		return false, err
	}
	for i, t := range all.Result.Tabs {
		if t.ID == got.Result.Tab.ID {
			return got.Result.Tab.Label == strconv.Itoa(i+1), nil
		}
	}
	return false, fmt.Errorf("herdr tab list: tab %s is not in its workspace's list", got.Result.Tab.ID)
}

// TabWorkspace returns the workspace id from `herdr tab get`'s answer.
func TabWorkspace(tab []byte) (string, error) {
	var got struct {
		Result struct {
			Tab *struct {
				Workspace string `json:"workspace_id"`
			} `json:"tab"`
		} `json:"result"`
	}
	if err := json.Unmarshal(tab, &got); err != nil {
		return "", err
	}
	if got.Result.Tab == nil || got.Result.Tab.Workspace == "" {
		return "", errors.New("herdr tab get: answer has no result.tab.workspace_id")
	}
	return got.Result.Tab.Workspace, nil
}
