package agent

import (
	"strings"
	"testing"
)

func TestReadMeminfoUsedMB(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMB  int
		wantErr bool
	}{
		{
			name: "typical meminfo computes total minus available in MB",
			// 24117248 kB total - 16777216 kB available = 7340032 kB = 7168 MB
			content: "MemTotal:       24117248 kB\n" +
				"MemFree:         1048576 kB\n" +
				"MemAvailable:   16777216 kB\n" +
				"Buffers:          262144 kB\n",
			wantMB: 7168,
		},
		{
			name: "MemAvailable can appear before MemTotal",
			content: "MemAvailable:   16777216 kB\n" +
				"MemTotal:       24117248 kB\n",
			wantMB: 7168,
		},
		{
			name:    "missing MemAvailable is an error (fail-open upstream)",
			content: "MemTotal:       24117248 kB\nMemFree: 1048576 kB\n",
			wantErr: true,
		},
		{
			name:    "missing MemTotal is an error",
			content: "MemAvailable:   16777216 kB\n",
			wantErr: true,
		},
		{
			name:    "empty input is an error",
			content: "",
			wantErr: true,
		},
		{
			name: "available greater than total clamps to zero, never negative",
			content: "MemTotal:        1048576 kB\n" +
				"MemAvailable:    2097152 kB\n",
			wantMB: 0,
		},
		{
			name: "garbage value for the field is treated as absent",
			content: "MemTotal:       not-a-number kB\n" +
				"MemAvailable:   16777216 kB\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readMeminfoUsedMB(strings.NewReader(tt.content))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got usedMB=%d nil err", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantMB {
				t.Errorf("usedMB = %d, want %d", got, tt.wantMB)
			}
		})
	}
}

func TestParseMeminfoKB(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		key    string
		wantV  int64
		wantOK bool
	}{
		{"matching key with kB suffix", "MemTotal:       24117248 kB", "MemTotal:", 24117248, true},
		{"matching key no suffix", "MemAvailable: 100", "MemAvailable:", 100, true},
		{"non-matching key", "MemFree: 1048576 kB", "MemTotal:", 0, false},
		{"prefix collision is not a false match", "MemTotalSwap: 5 kB", "MemTotal:", 0, false},
		{"empty value after key", "MemTotal:", "MemTotal:", 0, false},
		{"non-numeric value", "MemTotal: xyz kB", "MemTotal:", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := parseMeminfoKB(tt.line, tt.key)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && v != tt.wantV {
				t.Errorf("value = %d, want %d", v, tt.wantV)
			}
		})
	}
}
