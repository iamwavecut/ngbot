package text

// HasCyrillics checks if the given string contains any Cyrillic characters
func HasCyrillics(content string) bool {
	for _, r := range content {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// NormalizeCyrillics normalizes Cyrillic text by removing special characters and extra spaces
func NormalizeCyrillics(content string) string {
	var result []rune
	var lastWasSpace bool
	for _, r := range content {
		if r >= 0x0400 && r <= 0x04FF || r == ' ' {
			if r == ' ' {
				if !lastWasSpace {
					result = append(result, r)
					lastWasSpace = true
				}
			} else {
				result = append(result, r)
				lastWasSpace = false
			}
		}
	}
	return string(result)
}
