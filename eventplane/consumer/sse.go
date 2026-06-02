package consumer

import (
	"bufio"
	"io"
	"strings"
)

// maxFrameBytes bounds a single SSE line so a hostile or buggy producer cannot
// drive unbounded memory growth. Event payloads here are small contact snapshots
// (§8.6); 1 MiB is generous headroom.
const maxFrameBytes = 1 << 20

// sseFrame is one fully-received SSE frame: a complete record terminated by a
// blank line (§8.1, §10.1). The consumer dispatches a frame ONLY once its data
// buffer is non-empty at the blank-line terminator — an event:-only frame with
// no data: line is silently dropped by the SSE rules (§10.1), and a frame that
// the connection dropped before its terminator is never emitted at all (§10.1:
// "a frame received incomplete ... MUST be discarded without advancing").
type sseFrame struct {
	id    string // the opaque cursor on the id: line; "" for control/liveness frames (§8.2)
	event string // the event: line, e.g. "contact.created", "caught-up", "resync", "status"
	data  string // the data: payload, multiple data: lines joined with "\n"
}

// scanFrames reads SSE frames from r and invokes fn for each fully-received
// frame, in order. It is a hand-rolled parser (decision 15, ~no dependency):
// a bufio.Scanner over the body accumulates id:/event:/data: fields and
// dispatches on the blank line, skipping comment lines (": keepalive", §10.1).
// Multi-line data: accumulation is implemented (joined with "\n") for robustness
// even though this producer emits single-line data (§8.1).
//
// scanFrames returns when fn returns a non-nil error (propagated), or when r is
// exhausted/errors (the underlying read error, or nil on a clean EOF). A partial
// frame buffered at EOF is dropped without dispatch — exactly the §10.1 rule that
// a half-frame never advances the cursor.
func scanFrames(r io.Reader, fn func(sseFrame) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)

	var (
		cur     sseFrame
		dataBuf []string
		sawData bool
	)
	reset := func() {
		cur = sseFrame{}
		dataBuf = dataBuf[:0]
		sawData = false
	}

	for sc.Scan() {
		line := sc.Text()

		if line == "" {
			// Blank line: dispatch the accumulated frame. Per §10.1 a frame is
			// dispatched only if a data: field was seen; an event:-only frame is
			// dropped.
			if sawData {
				cur.data = strings.Join(dataBuf, "\n")
				if err := fn(cur); err != nil {
					return err
				}
			}
			reset()
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment line (e.g. ": keepalive", §10.1) — proves liveness, carries
			// no field. Ignore.
			continue
		}

		field, value := splitField(line)
		switch field {
		case "id":
			cur.id = value
		case "event":
			cur.event = value
		case "data":
			dataBuf = append(dataBuf, value)
			sawData = true
		default:
			// Unknown SSE field (e.g. "retry") — ignore; not part of this contract.
		}
	}
	return sc.Err()
}

// splitField splits one SSE line "field: value" into its field name and value,
// stripping a single leading space from the value per the SSE wire rules. A line
// with no colon is a field with an empty value.
func splitField(line string) (field, value string) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line, ""
	}
	field = line[:i]
	value = line[i+1:]
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	return field, value
}
