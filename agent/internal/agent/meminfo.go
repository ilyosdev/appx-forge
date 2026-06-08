package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// meminfoPath is the Linux procfs file exposing host memory statistics.
// Overridable in tests; the agent only ever runs on Linux sandbox hosts.
const meminfoPath = "/proc/meminfo"

// hostUsedMemoryMB returns the non-reclaimable memory currently in use across
// the whole host, in MB, computed as (MemTotal - MemAvailable) from
// /proc/meminfo. "Used" here is everything the kernel cannot hand to a new
// allocation without reclaim — the OS, dockerd, the agent, and every
// sandbox's processes (including Metro). MemAvailable already discounts
// reclaimable page cache, so this is the honest pressure figure the scheduler
// should pack against (free = CapacityMB - UsedMB).
func hostUsedMemoryMB() (int, error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return readMeminfoUsedMB(f)
}

// readMeminfoUsedMB parses /proc/meminfo content and returns used memory in MB,
// i.e. (MemTotal - MemAvailable). meminfo reports values in kB. It returns an
// error if either field is absent (e.g. a non-Linux or stripped /proc) so the
// caller can fail open rather than report a bogus number.
func readMeminfoUsedMB(r io.Reader) (int, error) {
	var totalKB, availKB int64
	var haveTotal, haveAvail bool

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := parseMeminfoKB(line, "MemTotal:"); ok {
			totalKB, haveTotal = v, true
		} else if v, ok := parseMeminfoKB(line, "MemAvailable:"); ok {
			availKB, haveAvail = v, true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if !haveTotal || !haveAvail {
		return 0, fmt.Errorf("meminfo: missing MemTotal/MemAvailable")
	}

	usedKB := totalKB - availKB
	if usedKB < 0 {
		usedKB = 0
	}
	return int(usedKB / 1024), nil
}

// parseMeminfoKB extracts the kB value from a "<key> <number> kB" meminfo line.
// Returns (value, true) only when the line starts with key and the number
// parses; otherwise (0, false).
func parseMeminfoKB(line, key string) (int64, bool) {
	if !strings.HasPrefix(line, key) {
		return 0, false
	}
	fields := strings.Fields(line[len(key):])
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
