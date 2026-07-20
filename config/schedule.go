// Package config: schedule.go defines a loop's optional repeating
// cron-style trigger and normalizes it into robfig/cron/v3 spec strings.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule declares a loop's repeating trigger. Exactly one of Every, At,
// or Cron must be set; CronSpecs enforces this.
type Schedule struct {
	Every string   `yaml:"every,omitempty"` // duration shorthand: "15m", "2h", "1h30m"
	At    []string `yaml:"at,omitempty"`    // daily times: ["09:00", "14:00", "20:00"]
	Cron  string   `yaml:"cron,omitempty"`  // raw 5-field cron expression
}

// CronSpecs normalizes s into one or more github.com/robfig/cron/v3 spec
// strings, each suitable for (*cron.Cron).AddFunc. Every and Cron always
// produce exactly one spec; At produces one spec per entry (never a single
// comma-joined spec, which would wrongly cross-product distinct
// hour/minute pairs — e.g. at: ["09:15","14:30"] must fire at 9:15 and
// 14:30, not also 9:30 and 14:15).
func (s *Schedule) CronSpecs() ([]string, error) {
	set := 0
	if s.Every != "" {
		set++
	}
	if len(s.At) > 0 {
		set++
	}
	if s.Cron != "" {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("schedule must set exactly one of every, at, or cron (got %d set)", set)
	}

	switch {
	case s.Every != "":
		d, err := time.ParseDuration(s.Every)
		if err != nil {
			return nil, fmt.Errorf("invalid schedule.every %q: %w", s.Every, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("schedule.every must be positive, got %q", s.Every)
		}
		return []string{"@every " + d.String()}, nil

	case len(s.At) > 0:
		specs := make([]string, 0, len(s.At))
		for _, t := range s.At {
			hour, minute, err := parseClockTime(t)
			if err != nil {
				return nil, fmt.Errorf("invalid schedule.at %q: %w", t, err)
			}
			specs = append(specs, fmt.Sprintf("%d %d * * *", minute, hour))
		}
		return specs, nil

	default:
		_, err := cron.ParseStandard(s.Cron)
		if err != nil {
			return nil, fmt.Errorf("invalid schedule.cron %q: %w", s.Cron, err)
		}
		return []string{s.Cron}, nil
	}
}

// parseClockTime parses "HH:MM" (24-hour, no seconds) into its hour and
// minute components.
func parseClockTime(t string) (hour, minute int, err error) {
	parts := strings.SplitN(t, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want HH:MM")
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return hour, minute, nil
}
