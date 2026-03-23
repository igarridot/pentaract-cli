package app

import "testing"

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1048576, "1.00 MiB"},
		{1572864, "1.50 MiB"},
		{1073741824, "1.00 GiB"},
		{1099511627776, "1.00 TiB"},
		{5368709120, "5.00 GiB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
