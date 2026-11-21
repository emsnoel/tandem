package main

// How an agy reset is confirmed.
//
// agy's /new (an alias of /clear) opens a new conversation, but Herdr learns
// of it only on the next turn, and /clear runs no lifecycle Herdr could see.
// agy's process log records the new conversation at once (agy 1.5.14):
//
// 	I1121 16:17:37.338116    1179 server.go:1269] Created conversation e76daff4-... (backend=...)
//
// Each process writes log/cli-<start>.log under agy's home, and the log's first
// line names the process ("Starting language server process with pid N"), so
// the pane's pid picks its log. The timestamps are local time with no year.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

var (
	agyLogPid     = regexp.MustCompile(`\] Starting language server process with pid (\d+)$`)
	agyLogCreated = regexp.MustCompile(`^I(\d{4} \d{2}:\d{2}:\d{2}\.\d+) .*\] Created conversation ([0-9A-Za-z][0-9A-Za-z-]*) `)
)

// agyFreshSession is the conversation the agy in pane created at or after
// since, "" when its log shows none.
func agyFreshSession(home, pane string, since time.Time) string {
	raw, err := herdr.Raw("pane", "process-info", "--pane", pane)
	if err != nil {
		return ""
	}
	info, err := decodeProcessInfo(raw)
	if err != nil {
		return ""
	}
	pid := info.pidOf("agy")
	if pid == 0 {
		return ""
	}
	return agyLogSession(home, pid, since)
}

// agyLogSession finds the log of agy process pid under home and returns the
// last conversation it created at or after since.
func agyLogSession(home string, pid int, since time.Time) string {
	logs, _ := filepath.Glob(filepath.Join(home, "log", "cli-*.log"))
	for _, path := range logs {
		if agyLogOwner(path) == pid {
			return agyLogCreatedSince(path, since)
		}
	}
	return ""
}

// agyLogOwner is the pid the log's first line names, 0 when it names none.
func agyLogOwner(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0
	}
	m := agyLogPid.FindStringSubmatch(sc.Text())
	if m == nil {
		return 0
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

func agyLogCreatedSince(path string, since time.Time) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	found := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		m := agyLogCreated.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ts, err := time.ParseInLocation("0102 15:04:05.000000", m[1], since.Location())
		if err != nil {
			continue
		}
		ts = ts.AddDate(since.Year(), 0, 0)
		if ts.Before(since) {
			continue
		}
		found = m[2]
	}
	return found
}
