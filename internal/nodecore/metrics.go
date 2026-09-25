package nodecore

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	metricAvailability = "nodecore_upstream_availability_status"
	metricHeadLag      = "nodecore_upstream_head_lag"
	metricHead         = "nodecore_upstream_heads"
	metricStartTime    = "process_start_time_seconds"
)

// upstreamMetrics is what nodecore reports about one upstream.
type upstreamMetrics struct {
	chain     string
	status    float64
	hasStatus bool
	headLag   float64
	hasLag    bool
	head      float64
	hasHead   bool
}

type snapshot struct {
	// startTime is when the nodecore process started (zero if not reported).
	startTime time.Time
	upstreams map[string]*upstreamMetrics
}

// parseSnapshot extracts upstream metrics from the Prometheus text format.
func parseSnapshot(r io.Reader) (*snapshot, error) {
	snap := &snapshot{upstreams: make(map[string]*upstreamMetrics)}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, labels, value, err := parseSample(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}

		if name == metricStartTime {
			sec, frac := math.Modf(value)
			snap.startTime = time.Unix(int64(sec), int64(frac*1e9))
			continue
		}
		if name != metricAvailability && name != metricHeadLag && name != metricHead {
			continue
		}

		id := labels["upstream"]
		if id == "" {
			continue
		}
		u := snap.upstreams[id]
		if u == nil {
			u = &upstreamMetrics{}
			snap.upstreams[id] = u
		}
		if chain := labels["chain"]; chain != "" {
			u.chain = chain
		}
		switch name {
		case metricAvailability:
			u.status, u.hasStatus = value, true
		case metricHeadLag:
			u.headLag, u.hasLag = value, true
		case metricHead:
			u.head, u.hasHead = value, true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read metrics: %w", err)
	}

	return snap, nil
}

// parseSample parses one sample line: name[{labels}] value [timestamp].
func parseSample(line string) (string, map[string]string, float64, error) {
	var (
		name   string
		labels map[string]string
		rest   string
	)

	if i := strings.IndexAny(line, "{ \t"); i < 0 {
		return "", nil, 0, fmt.Errorf("sample without value: %q", line)
	} else if line[i] == '{' {
		name = line[:i]
		var err error
		labels, rest, err = parseLabels(line[i+1:])
		if err != nil {
			return "", nil, 0, err
		}
	} else {
		name, rest = line[:i], line[i:]
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil, 0, fmt.Errorf("sample without value: %q", line)
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid value %q: %w", fields[0], err)
	}
	return name, labels, value, nil
}

// parseLabels parses `key="value",...}` and returns the text after '}'.
func parseLabels(s string) (map[string]string, string, error) {
	labels := make(map[string]string)
	for {
		s = strings.TrimLeft(s, " \t")
		if strings.HasPrefix(s, "}") {
			return labels, s[1:], nil
		}

		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, "", fmt.Errorf("malformed label in %q", s)
		}
		key := strings.TrimSpace(s[:eq])
		s = strings.TrimLeft(s[eq+1:], " \t")
		if !strings.HasPrefix(s, `"`) {
			return nil, "", fmt.Errorf("malformed label %q: value must be quoted", key)
		}
		s = s[1:]

		var value strings.Builder
		closed := false
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' && i+1 < len(s):
				i++
				switch s[i] {
				case 'n':
					value.WriteByte('\n')
				default:
					value.WriteByte(s[i])
				}
			case c == '"':
				s, closed = s[i+1:], true
			default:
				value.WriteByte(c)
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, "", fmt.Errorf("unterminated label value for %q", key)
		}
		labels[key] = value.String()

		s = strings.TrimLeft(s, " \t")
		s = strings.TrimPrefix(s, ",")
	}
}
