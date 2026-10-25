package main

// How a Copilot reset is confirmed.
//
// Copilot's /new opens a new conversation, but Herdr keeps reporting the
// launch --session-id and its counter does not move. Copilot's process log
// records the new conversation at once:
//
// 	2026-10-11T20:57:37.240Z [INFO] Registering foreground session: 61550360-...
//
// Each process writes logs/process-<ms>-<pid>.log under its home, so the pane's
// pid picks its log. A line in any other format leaves the reset unconfirmed.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

var copilotRegistered = regexp.MustCompile(`^(\S+) \[INFO\] Registering foreground session: ([0-9A-Za-z][0-9A-Za-z-]*)$`)

// copilotFreshSession is the conversation the Copilot in pane opened at or
// after since, "" when its log shows none.
func copilotFreshSession(home, pane string, since time.Time) string {
	raw, err := herdr.Raw("pane", "process-info", "--pane", pane)
	if err != nil {
		return ""
	}
	info, err := decodeProcessInfo(raw)
	if err != nil {
		return ""
	}
	pid := info.pidOf("copilot")
	if pid == 0 {
		return ""
	}
	return copilotLogSession(home, pid, since)
}

// copilotLogSession reads the newest log of Copilot process pid under home and
// returns the last conversation it registered at or after since.
func copilotLogSession(home string, pid int, since time.Time) string {
	logs, _ := filepath.Glob(filepath.Join(home, "logs", fmt.Sprintf("process-*-%d.log", pid)))
	if len(logs) == 0 {
		return ""
	}
	// A pid can be reused; the newest log is the running process's.
	sort.Slice(logs, func(i, j int) bool { return modTime(logs[i]).After(modTime(logs[j])) })
	f, err := os.Open(logs[0])
	if err != nil {
		return ""
	}
	defer f.Close()
	found := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		m := copilotRegistered.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil || ts.Before(since) {
			continue
		}
		found = m[2]
	}
	return found
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// processInfo is what `herdr pane process-info` says about a pane's processes.
type processInfo struct {
	Shell      *int `json:"shell_pid"`
	Foreground *int `json:"foreground_process_group_id"`
	Processes  []struct {
		Name string `json:"name"`
		Pid  int    `json:"pid"`
	} `json:"foreground_processes"`
}

func decodeProcessInfo(raw []byte) (processInfo, error) {
	var r struct {
		Result struct {
			Info processInfo `json:"process_info"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return processInfo{}, err
	}
	if r.Result.Info.Shell == nil || r.Result.Info.Foreground == nil {
		return processInfo{}, errors.New("no process information")
	}
	return r.Result.Info, nil
}

// pidOf is the pid of the pane's foreground process named name, 0 when none.
func (p processInfo) pidOf(name string) int {
	for _, proc := range p.Processes {
		if proc.Name == name {
			return proc.Pid
		}
	}
	return 0
}
