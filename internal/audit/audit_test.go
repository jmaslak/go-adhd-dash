package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return time.Date(2026, 9, 29, 20, 50, 1, 0, time.FixedZone("MDT", -6*3600)) }
	l.Record(Login, F("user", "bob"), F("lu", "AD000002"), F("ip", "192.168.1.5"))
	l.Record(Disconnect, F("user", "bob"), F("reason", "terminated by an administrator"), F("x", ""), F("y", `a"b=c`))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.Record(Logout, F("user", "late")) // closed: goes to the server's log instead

	// Appends, never truncates.
	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC) }
	l.Record(Logout, F("user", "bob"))
	l.Close() //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`2026-09-29T20:50:01-06:00 LOGIN user=bob lu=AD000002 ip=192.168.1.5`,
		`2026-09-29T20:50:01-06:00 DISCONNECT user=bob reason="terminated by an administrator" x="" y="a\"b=c"`,
		`2026-09-29T21:00:00Z LOGOUT user=bob`,
		``,
	}, "\n")
	if string(data) != want {
		t.Errorf("log:\n%s\nwant:\n%s", data, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}

	var nilLog *Log
	nilLog.Record(Login)
	if nilLog.Close() != nil {
		t.Error("nil log close failed")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "no", "such", "dir", "a.log")); err == nil {
		t.Error("opened a log in a directory that does not exist")
	}
}
