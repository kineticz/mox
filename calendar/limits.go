package calendar

import (
	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mox-"
)

// Limits returns the configured calendar limits with defaults applied.
func Limits() config.CalendarFile {
	limits := mox.Conf.Calendar
	mox.ApplyCalendarDefaults(&limits)
	return limits
}
