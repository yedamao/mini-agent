// Package buggy has a small, deliberate bug for the agent to fix (Day 5).
package buggy

import "strings"

// ReverseWords returns the words of s in reverse order, separated by single spaces.
// Example: "hello big world" -> "world big hello".
func ReverseWords(s string) string {
	words := strings.Fields(s)
	out := make([]string, 0, len(words))
	for i := len(words) - 1; i > 0; i-- {
		out = append(out, words[i])
	}
	return strings.Join(out, " ")
}
