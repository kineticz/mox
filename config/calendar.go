package config

// CalendarFile is the parsed form of calendar.conf.
type CalendarFile struct {
	EnableCalendar  bool `sconf:"optional" sconf-doc:"Enable the server-side calendar store, calendar JSON API and default per-account calendar. Default: false."`
	EnableCalDAV    bool `sconf:"optional" sconf-doc:"Enable CalDAV. Requires EnableCalendar. Default: false. Unused in phase 1."`
	EnableIMIP      bool `sconf:"optional" sconf-doc:"Enable iTIP/iMIP scheduling over email. Requires EnableCalendar. Default: false. Unused in phase 1."`
	EnableScheduled bool `sconf:"optional" sconf-doc:"Enable the ScheduledMessage service and virtual IMAP Scheduled mailbox. Default: false. Unused in phase 1."`

	MaxICSBytes            int `sconf:"optional" sconf-doc:"Maximum iCalendar input size in bytes. Default: 1048576 (1 MiB)."`
	MaxComponents          int `sconf:"optional" sconf-doc:"Maximum VEVENT components per calendar object. Default: 64."`
	MaxAttendees           int `sconf:"optional" sconf-doc:"Maximum ATTENDEE properties per event. Default: 64."`
	MaxExceptions          int `sconf:"optional" sconf-doc:"Maximum exception instances (RECURRENCE-ID rows plus EXDATE values) per series. Default: 256."`
	MaxRecurrenceInstances int `sconf:"optional" sconf-doc:"Maximum expanded instances returned for one series in one query. Default: 520."`
	ExpansionHorizonDays   int `sconf:"optional" sconf-doc:"Maximum days a recurrence may be expanded from the query start. Default: 730."`
}
