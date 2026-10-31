package herdr

import (
	"encoding/json"
	"errors"
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

// TabIsDefault reports whether `herdr tab get`'s answer shows a tab still
// labelled with its own number, the label Herdr gives a tab nobody named.
func TabIsDefault(out []byte) (bool, error) {
	var env struct {
		Result struct {
			Tab *struct {
				Label  string `json:"label"`
				Number int    `json:"number"`
			} `json:"tab"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return false, err
	}
	if env.Result.Tab == nil {
		return false, errors.New("herdr tab get: answer has no result.tab")
	}
	return env.Result.Tab.Label == strconv.Itoa(env.Result.Tab.Number), nil
}
