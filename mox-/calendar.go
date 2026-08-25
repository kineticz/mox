package mox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mjl-/sconf"

	"github.com/mjl-/mox/config"
)

func LoadCalendarConfig(dir string) (config.CalendarFile, bool, error) {
	p := filepath.Join(dir, "calendar.conf")
	var cf config.CalendarFile
	err := sconf.ParseFile(p, &cf)
	if err != nil {
		if os.IsNotExist(err) {
			return config.CalendarFile{}, false, nil
		}
		return config.CalendarFile{}, true, fmt.Errorf("parsing %s: %w", p, err)
	}
	return cf, true, nil
}

func ApplyCalendarDefaults(c *config.CalendarFile) {
	if c.MaxICSBytes == 0 {
		c.MaxICSBytes = 1048576
	}
	if c.MaxComponents == 0 {
		c.MaxComponents = 64
	}
	if c.MaxAttendees == 0 {
		c.MaxAttendees = 64
	}
	if c.MaxExceptions == 0 {
		c.MaxExceptions = 256
	}
	if c.MaxRecurrenceInstances == 0 {
		c.MaxRecurrenceInstances = 520
	}
	if c.ExpansionHorizonDays == 0 {
		c.ExpansionHorizonDays = 730
	}
}

func ValidateCalendarFile(c config.CalendarFile) error {
	if (c.EnableCalDAV || c.EnableIMIP) && !c.EnableCalendar {
		return fmt.Errorf("%w: EnableCalDAV/EnableIMIP require EnableCalendar", ErrConfig)
	}
	if c.MaxICSBytes < 0 || c.MaxComponents < 0 || c.MaxAttendees < 0 || c.MaxExceptions < 0 || c.MaxRecurrenceInstances < 0 || c.ExpansionHorizonDays < 0 {
		return fmt.Errorf("%w: calendar limits must not be negative", ErrConfig)
	}
	return nil
}

func CalendarEnabled() bool {
	return Conf.Calendar.EnableCalendar
}

func CalDAVEnabled() bool {
	return Conf.Calendar.EnableCalendar && Conf.Calendar.EnableCalDAV
}
