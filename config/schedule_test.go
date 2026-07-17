package config

import (
	"strings"
	"testing"
)

func TestSchedule_CronSpecs_Every(t *testing.T) {
	s := &Schedule{Every: "15m"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "@every 15m0s" {
		t.Errorf("specs = %v, want [\"@every 15m0s\"]", specs)
	}
}

func TestSchedule_CronSpecs_EveryCompoundDuration(t *testing.T) {
	s := &Schedule{Every: "1h30m"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "@every 1h30m0s" {
		t.Errorf("specs = %v, want [\"@every 1h30m0s\"]", specs)
	}
}

func TestSchedule_CronSpecs_EveryInvalidDuration(t *testing.T) {
	s := &Schedule{Every: "not-a-duration"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded, want an error for an invalid duration")
	}
}

func TestSchedule_CronSpecs_EveryRejectsNonPositive(t *testing.T) {
	s := &Schedule{Every: "0s"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded, want an error for a non-positive duration")
	}
}

func TestSchedule_CronSpecs_AtSingleTime(t *testing.T) {
	s := &Schedule{At: []string{"09:00"}}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "0 9 * * *" {
		t.Errorf("specs = %v, want [\"0 9 * * *\"]", specs)
	}
}

func TestSchedule_CronSpecs_AtMultipleTimesProducesOneSpecEach(t *testing.T) {
	// A naive comma-joined single spec ("0,30 9,14 * * *") would wrongly
	// cross-product to 9:00, 9:30, 14:00, 14:30. Each `at` entry must
	// become its own independent spec.
	s := &Schedule{At: []string{"09:15", "14:30"}}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0] != "15 9 * * *" || specs[1] != "30 14 * * *" {
		t.Errorf("specs = %v, want [\"15 9 * * *\", \"30 14 * * *\"]", specs)
	}
}

func TestSchedule_CronSpecs_AtInvalidTime(t *testing.T) {
	for _, bad := range []string{"9am", "25:00", "09:60", "09"} {
		s := &Schedule{At: []string{bad}}
		if _, err := s.CronSpecs(); err == nil {
			t.Errorf("CronSpecs(%q) succeeded, want an error", bad)
		}
	}
}

func TestSchedule_CronSpecs_CronPassthrough(t *testing.T) {
	s := &Schedule{Cron: "0 9 * * 1-5"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "0 9 * * 1-5" {
		t.Errorf("specs = %v, want [\"0 9 * * 1-5\"]", specs)
	}
}

func TestSchedule_CronSpecs_RejectsZeroSet(t *testing.T) {
	s := &Schedule{}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with no field set, want an error")
	}
}

func TestSchedule_CronSpecs_RejectsMultipleSet(t *testing.T) {
	s := &Schedule{Every: "15m", Cron: "0 9 * * *"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with two fields set, want an error")
	}
	s2 := &Schedule{Every: "15m", At: []string{"09:00"}}
	if _, err := s2.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with two fields set, want an error")
	}
}

func TestSchedule_CronSpecs_ErrorMentionsWhichFieldFailed(t *testing.T) {
	s := &Schedule{Every: "garbage"}
	_, err := s.CronSpecs()
	if err == nil || !strings.Contains(err.Error(), "every") {
		t.Errorf("err = %v, want it to mention \"every\"", err)
	}
}
