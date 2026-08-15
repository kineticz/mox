package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
)

func TestEnsureDefaultCalendar(t *testing.T) {
	log := mlog.New("store", nil)
	os.RemoveAll("../testdata/store/data")
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/store/mox.conf")
	mox.MustLoadConfig(true, false)
	mox.Conf.Calendar.EnableCalendar = true
	err := Init(ctxbg)
	tcheck(t, err, "init")
	defer func() {
		err := Close()
		tcheck(t, err, "close")
	}()
	defer Switchboard()()

	acc, err := OpenAccount(log, "mjl", false)
	tcheck(t, err, "open account")
	defer func() {
		err = acc.Close()
		tcheck(t, err, "closing account")
		acc.WaitClosed()
	}()

	var n int
	err = acc.DB.Read(ctxbg, func(tx *bstore.Tx) error {
		list, err := bstore.QueryTx[Calendar](tx).List()
		n = len(list)
		if n == 1 && list[0].Name != "Calendar" {
			t.Fatalf("%#v", list[0])
		}
		return err
	})
	tcheck(t, err, "list calendars")
	if n != 1 {
		t.Fatalf("default calendar count %d", n)
	}
}

func TestNoCalendarWhenDisabled(t *testing.T) {
	log := mlog.New("store", nil)
	os.RemoveAll("../testdata/store/data")
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/store/mox.conf")
	mox.MustLoadConfig(true, false)
	err := Init(ctxbg)
	tcheck(t, err, "init")
	defer func() {
		err := Close()
		tcheck(t, err, "close")
	}()
	defer Switchboard()()

	acc, err := OpenAccount(log, "mjl", false)
	tcheck(t, err, "open account")
	defer func() {
		err = acc.Close()
		tcheck(t, err, "closing account")
		acc.WaitClosed()
	}()

	err = acc.DB.Read(ctxbg, func(tx *bstore.Tx) error {
		list, err := bstore.QueryTx[Calendar](tx).List()
		if len(list) != 0 {
			t.Fatalf("unexpected calendars %d", len(list))
		}
		return err
	})
	tcheck(t, err, "list calendars")
}
