package store

import (
	cryptorand "crypto/rand"
	"fmt"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/mox-"
)

type Calendar struct {
	ID   int64
	Name string `bstore:"nonzero"`
	UID  string `bstore:"nonzero,unique"`
	CTag int64
}

type CalendarAttendee struct {
	Address  string
	Name     string
	Role     string
	PartStat string
	RSVP     bool
}

type CalendarEvent struct {
	ID           int64
	CalendarID   int64  `bstore:"ref Calendar,index"`
	UID          string `bstore:"nonzero,unique UID+RecurrenceID"`
	RecurrenceID string
	Sequence     int
	Revision     int64     `bstore:"nonzero"`
	Created      time.Time `bstore:"default now"`
	LastModified time.Time `bstore:"default now"`
	Organizer    string
	Summary      string
	Description  string
	Location     string
	Status       string
	Class        string
	StartUTC     time.Time `bstore:"index"`
	EndUTC       time.Time `bstore:"index"`
	TZID         string
	DateOnly     bool
	Floating     bool
	RRULE        string
	RDATE        []string
	EXDATE       []string
	Attendees    []CalendarAttendee
	ICS          []byte `bstore:"nonzero"`
}

func (a *Account) DefaultCalendar(tx *bstore.Tx) (Calendar, error) {
	return bstore.QueryTx[Calendar](tx).Limit(1).Get()
}

func ensureDefaultCalendar(tx *bstore.Tx) error {
	if !mox.CalendarEnabled() {
		return nil
	}

	_, err := bstore.QueryTx[Calendar](tx).Limit(1).Get()
	if err == nil {
		return nil
	}
	if err != bstore.ErrAbsent {
		return err
	}

	var random [16]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return fmt.Errorf("generating calendar uid: %w", err)
	}
	return tx.Insert(&Calendar{
		Name: "Calendar",
		UID:  fmt.Sprintf("calendar-%x", random),
	})
}
