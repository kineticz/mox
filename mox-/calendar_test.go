package mox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/sconf"
)

func TestLoadCalendarConfigAbsent(t *testing.T) {
	dir := t.TempDir()
	cf, existed, err := LoadCalendarConfig(dir)
	if err != nil {
		t.Fatalf("absent file: %v", err)
	}
	if existed {
		t.Fatalf("existed=true for missing file")
	}
	if cf.EnableCalendar || cf.MaxICSBytes != 0 {
		t.Fatalf("zero value expected, got %#v", cf)
	}
}

func TestLoadCalendarConfigValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "calendar.conf")
	if err := os.WriteFile(p, []byte("EnableCalendar: true\nMaxAttendees: 10\n"), 0660); err != nil {
		t.Fatal(err)
	}
	cf, existed, err := LoadCalendarConfig(dir)
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if !existed || !cf.EnableCalendar || cf.MaxAttendees != 10 {
		t.Fatalf("got %#v existed=%v", cf, existed)
	}
	ApplyCalendarDefaults(&cf)
	if cf.MaxICSBytes != 1048576 || cf.ExpansionHorizonDays != 730 {
		t.Fatalf("defaults: %#v", cf)
	}
}

func TestLoadCalendarConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "calendar.conf")
	if err := os.WriteFile(p, []byte("EnableCalendar: not-a-bool\n"), 0660); err != nil {
		t.Fatal(err)
	}
	_, existed, err := LoadCalendarConfig(dir)
	if err == nil || !existed {
		t.Fatalf("expected parse error, err=%v existed=%v", err, existed)
	}
}

func TestValidateCalendarFileCalDAVRequiresCalendar(t *testing.T) {
	err := ValidateCalendarFile(config.CalendarFile{EnableCalDAV: true})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestDescribeCalendarDoesNotAffectStatic(t *testing.T) {
	var buf bytes.Buffer
	if err := sconf.Describe(&buf, &config.CalendarFile{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "EnableCalendar") {
		t.Fatalf("describe: %s", buf.String())
	}
}

func TestParseConfigWithoutCalendarFileUnchanged(t *testing.T) {
	log := mlog.New("test", nil)
	c, errs := ParseConfig(context.Background(), log, filepath.FromSlash("../testdata/store/mox.conf"), true, false, false)
	if len(errs) != 0 {
		t.Fatalf("existing config without calendar.conf must still parse: %v", errs)
	}
	if c.Calendar.EnableCalendar {
		t.Fatalf("calendar must be disabled when calendar.conf is absent")
	}
}
