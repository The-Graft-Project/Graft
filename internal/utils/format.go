package utils

import (
	"fmt"
)

// FormatTimestamp formats a timestamp string from YYYYMMDDHHMMSS to DD/MM/YYYY | HH:MM:SS
func FormatTimestamp(ts string) string {
	if len(ts) != 14 {
		return ts
	}
	// YYYYMMDDHHMMSS
	// 01234567890123
	year := ts[0:4]
	month := ts[4:6]
	day := ts[6:8]
	hour := ts[8:10]
	minute := ts[10:12]
	second := ts[12:14]
	return fmt.Sprintf("%s/%s/%s | %s:%s:%s", day, month, year, hour, minute, second)
}
