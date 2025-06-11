package optimizer

import (
	"strconv"
	"strings"
	"time"
)

func MilitaryTimeToMinutes(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}

	/* try “3PM”, “3 PM” first */
	if strings.HasSuffix(s, "AM") || strings.HasSuffix(s, "PM") {
		if t, err := time.Parse("3PM", s); err == nil {
			return t.Hour() * 60
		}
		if t, err := time.Parse("3 PM", s); err == nil {
			return t.Hour() * 60
		}
	}

	/* HH or HH:MM 24-hour */
	if !strings.Contains(s, ":") {
		if h, err := strconv.Atoi(s); err == nil {
			return h * 60
		}
	}
	parts := strings.SplitN(s, ":", 2)
	h, _ := strconv.Atoi(parts[0])
	m := 0
	if len(parts) == 2 {
		m, _ = strconv.Atoi(parts[1])
	}
	return h*60 + m
}
