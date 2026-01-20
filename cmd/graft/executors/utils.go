package executors

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

func formatTimestamp(ts string) string {
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
func promptNewServer(reader *bufio.Reader) (string, int, string, string) {
	fmt.Print("Host IP: ")
	host, _ := reader.ReadString('\n')
	host = strings.TrimSpace(host)

	fmt.Print("Port (22): ")
	portStr, _ := reader.ReadString('\n')
	port, _ := strconv.Atoi(strings.TrimSpace(portStr))
	if port == 0 {
		port = 22
	}

	fmt.Print("User: ")
	user, _ := reader.ReadString('\n')
	user = strings.TrimSpace(user)

	fmt.Print("Key Path: ")
	keyPath, _ := reader.ReadString('\n')
	keyPath = strings.TrimSpace(keyPath)

	return host, port, user, keyPath
}
