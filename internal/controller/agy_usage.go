package controller

// Usage and conversation replies from Antigravity's agy CLI logs, measured
// against agy 1.5.14.
//
// agy writes one SQLite database per conversation to
// <home>/conversations/<conversationId>.db (with -wal and -shm).
//
// Token usage lives in gen_metadata(idx, data), one row per LLM call, where
// data is a protobuf message:
//   - f1 (ChatModelMetadata):
//     - f4 (ModelUsage, mirrored in f17.f2):
//       - f2 (varint): uncached input tokens (excludes cache reads)
//       - f3 (varint): output tokens (includes thinking tokens)
//       - f5 (varint): cache read input tokens (optional, 0 when absent)
//       - f7 (string): response id
//     - f19 (string): model identifier (fallback f21 display name)
//     - f20 (repeated map<string,string>): metadata including request_id
//
// Conversation steps live in steps(idx, step_type, status, ..., step_payload,
// step_format), where step_format is 0 in agy 1.5.14 and step_payload is a
// protobuf message:
//   - step_type 14 (user input): f19.f2 (or f19.f3.f1) holds the user prompt
//   - step_type 15 (planner response): f20.f8 (fallback f20.f1) holds the
//     visible assistant reply; f20.f3 is thinking and f20.f7 are tool calls
//   - step_type 101 (agent/system message): f114.f1 holds the message text
//
// When the SQLite schema, step_format, or gen_metadata wire shape is not
// recognized, the reader returns workflow.ErrUnsupported rather than guessing.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/workflow"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const (
	providerAgy  = "agy"
	agyMinMarker = 8

	agyStepUserInput       = 14
	agyStepPlannerResponse = 15
	agyStepAgentMessage    = 101
	agySupportedStepFormat = 0
)

// agySessionID is what an agy conversation id looks like. It names a file, so
// nothing else is ever put in a path.
var agySessionID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]*$`)

// DefaultAgyHome is where agy keeps its logs and conversations:
// ~/.gemini/antigravity-cli.
func DefaultAgyHome() string {
	return paths.AgyHome()
}

func agyConversationDB(home, session string) string {
	return filepath.Join(home, "conversations", session+".db")
}

// agyStreams returns the conversation's one stream, or workflow.ErrUnsupported
// when the database uses an unrecognized schema or format.
func agyStreams(home, session string) ([]rollout.Stream, error) {
	s := rollout.Stream{Provider: providerAgy, SessionID: session}
	if !agySessionID.MatchString(session) {
		s.Unavailable = "the session id is not an agy session id, so no log was looked for"
		return []rollout.Stream{s}, nil
	}
	if home == "" {
		s.Unavailable = "no agy home directory is configured"
		return []rollout.Stream{s}, nil
	}
	s.File = agyConversationDB(home, session)
	if _, err := os.Stat(s.File); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.Unavailable = "no agy log was found for this session"
		} else {
			s.Unavailable = "the log could not be read: " + err.Error()
		}
		return []rollout.Stream{s}, nil
	}
	db, err := openAgyDB(s.File)
	if err != nil {
		s.Unavailable = "the log could not be read: " + err.Error()
		return []rollout.Stream{s}, nil
	}
	defer db.Close()

	if err := checkAgySchema(db, true); err != nil {
		if errors.Is(err, workflow.ErrUnsupported) {
			return nil, err
		}
		s.Unavailable = "the log could not be read: " + err.Error()
		return []rollout.Stream{s}, nil
	}

	rows, err := db.Query(`SELECT idx, data FROM gen_metadata ORDER BY idx ASC`)
	if err != nil {
		s.Unavailable = "the log could not be read: " + err.Error()
		return []rollout.Stream{s}, nil
	}
	defer rows.Close()

	totalRows := 0
	unrecognizedRows := 0
	for rows.Next() {
		totalRows++
		var idx int
		var data []byte
		if err := rows.Scan(&idx, &data); err != nil {
			s.Unavailable, s.Deltas, s.Rejected = "reading the log failed: "+err.Error(), nil, 0
			return []rollout.Stream{s}, nil
		}
		usage, ok, err := parseAgyGenMetadata(data)
		if err != nil {
			s.Rejected++
			s.Diagnostics = append(s.Diagnostics, fmt.Sprintf("gen_metadata %d: %v; not counted", idx, err))
			continue
		}
		if !ok {
			unrecognizedRows++
			continue
		}
		if usage.uncachedInput > math.MaxInt64 || usage.cachedInput > math.MaxInt64 || usage.output > math.MaxInt64 ||
			usage.uncachedInput > math.MaxInt64-usage.cachedInput {
			s.Rejected++
			s.Diagnostics = append(s.Diagnostics, fmt.Sprintf("gen_metadata %d: token count overflows int64; not counted", idx))
			continue
		}
		totalInput := int64(usage.uncachedInput + usage.cachedInput)
		cached := int64(usage.cachedInput)
		output := int64(usage.output)
		id := usage.responseID
		if id == "" {
			id = usage.requestID
		}
		if id == "" {
			id = fmt.Sprintf("idx:%d", idx)
		}
		s.Deltas = append(s.Deltas, rollout.Delta{
			SourceID: agySourceID(session, id),
			Line:     idx + 1,
			Model:    usage.model,
			Counters: rollout.Counters{Input: totalInput, Cached: cached, Output: output},
		})
	}
	if err := rows.Err(); err != nil {
		s.Unavailable, s.Deltas, s.Rejected = "reading the log failed: "+err.Error(), nil, 0
		return []rollout.Stream{s}, nil
	}
	if len(s.Deltas) == 0 && s.Rejected == 0 {
		if totalRows > 0 && unrecognizedRows == totalRows {
			return nil, fmt.Errorf("%w: unrecognized agy gen_metadata protobuf format", workflow.ErrUnsupported)
		}
		s.Unavailable = "the log has no usage records"
		s.NoUsageEvents = true
	}
	return []rollout.Stream{s}, nil
}

func agySourceID(session, callID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|gen:%s", providerAgy, session, callID)))
	return hex.EncodeToString(sum[:16])
}

// agyReplies returns the visible assistant replies from one agy conversation
// database, oldest first. Thinking text and tool calls are excluded.
func agyReplies(path string) (string, error) {
	db, err := openAgyDB(path)
	if err != nil {
		return "", err
	}
	defer db.Close()

	if err := checkAgySchema(db, false); err != nil {
		return "", err
	}

	rows, err := db.Query(`SELECT step_payload FROM steps WHERE step_type = ? ORDER BY idx ASC`, agyStepPlannerResponse)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return "", err
		}
		if len(payload) == 0 {
			continue
		}
		reply, err := parseAgyPlannerReply(payload)
		if err != nil || reply == "" {
			continue
		}
		b.WriteString(reply)
		b.WriteString("\n")
	}
	return b.String(), rows.Err()
}

// agySessionContaining returns the agy conversation whose prompt held marker,
// among those written from a day before from, or "" when none did. Two matching
// conversations are an error, never a guess.
func agySessionContaining(home, marker string, from, to time.Time) (string, error) {
	if len(marker) < agyMinMarker {
		return "", fmt.Errorf("a marker of %d characters is too short to identify one conversation", len(marker))
	}
	if to.Before(from) {
		return "", fmt.Errorf("the interval ends before it starts")
	}
	if home == "" {
		return "", nil
	}
	logs, _ := filepath.Glob(filepath.Join(home, "conversations", "*.db"))
	sort.Strings(logs)
	since := from.AddDate(0, 0, -1)
	var found []string
	for _, p := range logs {
		name := filepath.Base(p)
		id := strings.TrimSuffix(name, ".db")
		if !agySessionID.MatchString(id) || agyModTime(p).Before(since) || !agyPromptHolds(p, marker) {
			continue
		}
		found = append(found, id)
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d conversations hold %s (%v), so the task's cannot be told", len(found), marker, found)
}

// agyModTime is the latest modification time of the database or its WAL file:
// SQLite WAL writes update .db-wal before checkpointing into .db.
func agyModTime(path string) time.Time {
	t := modTime(path)
	if wal := modTime(path + "-wal"); wal.After(t) {
		return wal
	}
	return t
}

// agyPromptHolds reports whether a user input (step_type 14) or agent message
// (step_type 101) step in the conversation holds marker. Assistant replies
// (step_type 15) are ignored so a nonce quoted back is never taken for a prompt.
func agyPromptHolds(path, marker string) bool {
	db, err := openAgyDB(path)
	if err != nil {
		return false
	}
	defer db.Close()

	if err := checkAgySchema(db, false); err != nil {
		return false
	}

	rows, err := db.Query(`SELECT step_type, step_payload FROM steps WHERE step_type IN (?, ?) ORDER BY idx ASC`, agyStepUserInput, agyStepAgentMessage)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var stepType int
		var payload []byte
		if err := rows.Scan(&stepType, &payload); err != nil {
			return false
		}
		if len(payload) == 0 {
			continue
		}
		text, err := parseAgyPromptText(stepType, payload)
		if err == nil && strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func openAgyDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// checkAgySchema verifies that db has the agy 1.5.14 SQLite tables, columns,
// and step_format (0). Unrecognized schemas return workflow.ErrUnsupported.
func checkAgySchema(db *sql.DB, requireGenMeta bool) error {
	tables := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('steps', 'gen_metadata')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if !tables["steps"] || (requireGenMeta && !tables["gen_metadata"]) {
		return fmt.Errorf("%w: agy conversation database is missing expected tables", workflow.ErrUnsupported)
	}
	if err := checkTableColumns(db, "steps", "idx", "step_type", "step_payload", "step_format"); err != nil {
		return err
	}
	if requireGenMeta {
		if err := checkTableColumns(db, "gen_metadata", "idx", "data"); err != nil {
			return err
		}
	}
	var badFormat int
	err = db.QueryRow(`SELECT step_format FROM steps WHERE step_format != ? LIMIT 1`, agySupportedStepFormat).Scan(&badFormat)
	if err == nil {
		return fmt.Errorf("%w: unrecognized agy step_format %d", workflow.ErrUnsupported, badFormat)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func checkTableColumns(db *sql.DB, table string, want ...string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range want {
		if !have[col] {
			return fmt.Errorf("%w: agy table %s is missing column %s", workflow.ErrUnsupported, table, col)
		}
	}
	return nil
}

// --- Minimal protobuf wire decoder for agy 1.5.14 blobs ---

type pbField struct {
	num    uint64
	wire   uint8
	varint uint64
	bytes  []byte
}

func decodeProto(b []byte) ([]pbField, error) {
	var fields []pbField
	pos := 0
	for pos < len(b) {
		tag, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("invalid protobuf tag at byte %d", pos)
		}
		pos += n
		num := tag >> 3
		wire := uint8(tag & 7)
		if num == 0 {
			return nil, fmt.Errorf("invalid protobuf field number 0")
		}
		f := pbField{num: num, wire: wire}
		switch wire {
		case 0: // varint
			v, m := binary.Uvarint(b[pos:])
			if m <= 0 {
				return nil, fmt.Errorf("invalid varint for field %d", num)
			}
			f.varint = v
			pos += m
		case 1: // 64-bit
			if pos+8 > len(b) {
				return nil, fmt.Errorf("truncated 64-bit field %d", num)
			}
			f.bytes = b[pos : pos+8]
			pos += 8
		case 2: // length-delimited
			ln, m := binary.Uvarint(b[pos:])
			if m <= 0 || ln > uint64(len(b)-(pos+m)) {
				return nil, fmt.Errorf("invalid length-delimited field %d", num)
			}
			pos += m
			f.bytes = b[pos : pos+int(ln)]
			pos += int(ln)
		case 5: // 32-bit
			if pos+4 > len(b) {
				return nil, fmt.Errorf("truncated 32-bit field %d", num)
			}
			f.bytes = b[pos : pos+4]
			pos += 4
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d for field %d", wire, num)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

type agyGenUsage struct {
	model         string
	responseID    string
	requestID     string
	uncachedInput uint64
	cachedInput   uint64
	output        uint64
}

// parseAgyGenMetadata extracts token counts and model identity from one
// gen_metadata.data blob. ok is false when the blob is valid protobuf but does
// not carry the agy 1.5.14 f1.f4 / f1.f17.f2 usage message.
func parseAgyGenMetadata(data []byte) (agyGenUsage, bool, error) {
	if len(data) == 0 {
		return agyGenUsage{}, false, nil
	}
	top, err := decodeProto(data)
	if err != nil {
		return agyGenUsage{}, false, err
	}
	var chatMetaBytes []byte
	for _, f := range top {
		if f.num == 1 && f.wire == 2 {
			chatMetaBytes = f.bytes
		}
	}
	if len(chatMetaBytes) == 0 {
		return agyGenUsage{}, false, nil
	}
	chatMeta, err := decodeProto(chatMetaBytes)
	if err != nil {
		return agyGenUsage{}, false, err
	}
	var (
		out         agyGenUsage
		displayName string
		usageBytes  []byte
		altWrapper  []byte
	)
	for _, f := range chatMeta {
		switch {
		case f.num == 4 && f.wire == 2:
			usageBytes = f.bytes
		case f.num == 17 && f.wire == 2:
			altWrapper = f.bytes
		case f.num == 19 && f.wire == 2:
			out.model = string(f.bytes)
		case f.num == 21 && f.wire == 2:
			displayName = string(f.bytes)
		case f.num == 20 && f.wire == 2:
			if k, v := parseProtoMapEntry(f.bytes); k == "request_id" && v != "" {
				out.requestID = v
			}
		}
	}
	if out.model == "" {
		out.model = displayName
	}
	if len(usageBytes) == 0 && len(altWrapper) > 0 {
		if alt, err := decodeProto(altWrapper); err == nil {
			for _, f := range alt {
				if f.num == 2 && f.wire == 2 {
					usageBytes = f.bytes
				}
			}
		}
	}
	if len(usageBytes) == 0 {
		return agyGenUsage{}, false, nil
	}
	usageFields, err := decodeProto(usageBytes)
	if err != nil {
		return agyGenUsage{}, false, err
	}
	var hasInput, hasOutput bool
	for _, f := range usageFields {
		switch {
		case f.num == 2 && f.wire == 0:
			out.uncachedInput = f.varint
			hasInput = true
		case f.num == 3 && f.wire == 0:
			out.output = f.varint
			hasOutput = true
		case f.num == 5 && f.wire == 0:
			out.cachedInput = f.varint
		case f.num == 7 && f.wire == 2:
			out.responseID = string(f.bytes)
		}
	}
	if !hasInput || !hasOutput {
		return agyGenUsage{}, false, fmt.Errorf("usage record missing input or output token count")
	}
	return out, true, nil
}

func parseProtoMapEntry(b []byte) (string, string) {
	fields, err := decodeProto(b)
	if err != nil {
		return "", ""
	}
	var k, v string
	for _, f := range fields {
		if f.num == 1 && f.wire == 2 {
			k = string(f.bytes)
		} else if f.num == 2 && f.wire == 2 {
			v = string(f.bytes)
		}
	}
	return k, v
}

// parseAgyPlannerReply extracts the visible assistant reply from a step_type 15
// step_payload (f20.f8, falling back to f20.f1).
func parseAgyPlannerReply(payload []byte) (string, error) {
	top, err := decodeProto(payload)
	if err != nil {
		return "", err
	}
	var plannerBytes []byte
	for _, f := range top {
		if f.num == 20 && f.wire == 2 {
			plannerBytes = f.bytes
		}
	}
	if len(plannerBytes) == 0 {
		return "", nil
	}
	planner, err := decodeProto(plannerBytes)
	if err != nil {
		return "", err
	}
	var response, modified string
	for _, f := range planner {
		if f.num == 1 && f.wire == 2 {
			response = string(f.bytes)
		} else if f.num == 8 && f.wire == 2 {
			modified = string(f.bytes)
		}
	}
	if modified != "" {
		return modified, nil
	}
	return response, nil
}

// parseAgyPromptText extracts the prompt text from a user input (step_type 14:
// f19.f2 or f19.f3.f1) or agent message (step_type 101: f114.f1) step_payload.
func parseAgyPromptText(stepType int, payload []byte) (string, error) {
	top, err := decodeProto(payload)
	if err != nil {
		return "", err
	}
	switch stepType {
	case agyStepUserInput:
		var userBytes []byte
		for _, f := range top {
			if f.num == 19 && f.wire == 2 {
				userBytes = f.bytes
			}
		}
		if len(userBytes) == 0 {
			return "", nil
		}
		userFields, err := decodeProto(userBytes)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, f := range userFields {
			if f.num == 2 && f.wire == 2 && len(f.bytes) > 0 {
				b.Write(f.bytes)
				b.WriteByte('\n')
			} else if f.num == 3 && f.wire == 2 && len(f.bytes) > 0 {
				if item, err := decodeProto(f.bytes); err == nil {
					for _, sf := range item {
						if sf.num == 1 && sf.wire == 2 && len(sf.bytes) > 0 {
							b.Write(sf.bytes)
							b.WriteByte('\n')
						}
					}
				}
			}
		}
		return b.String(), nil
	case agyStepAgentMessage:
		var msgBytes []byte
		for _, f := range top {
			if f.num == 114 && f.wire == 2 {
				msgBytes = f.bytes
			}
		}
		if len(msgBytes) == 0 {
			return "", nil
		}
		msgFields, err := decodeProto(msgBytes)
		if err != nil {
			return "", err
		}
		for _, f := range msgFields {
			if f.num == 1 && f.wire == 2 {
				return string(f.bytes), nil
			}
		}
	}
	return "", nil
}
