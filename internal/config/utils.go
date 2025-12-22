package config

import (
	"crypto/rand"
	"math/big"
	"regexp"
	"strings"
)

// GenerateRandomString generates a secure random string of specified length
func GenerateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// Fallback to simpler math if crypto fails (highly unlikely)
			result[i] = charset[i%len(charset)]
			continue
		}
		result[i] = charset[num.Int64()]
	}
	return string(result)
}

// IsValidProjectName checks if the name contains only lowercase alphanumeric characters and underscores
func IsValidProjectName(name string) bool {
	matched, _ := regexp.MatchString("^[a-z0-9_]+$", name)
	return matched
}

// NormalizeProjectName converts name to lowercase and replaces spaces/hyphens with underscores
func NormalizeProjectName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	// Remove any other non-alphanumeric/underscore characters
	reg := regexp.MustCompile("[^a-z0-9_]+")
	return reg.ReplaceAllString(name, "")
}
