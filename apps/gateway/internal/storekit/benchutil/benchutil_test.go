package benchutil

import "testing"

func TestCheckDSN(t *testing.T) {
	for _, c := range []struct {
		dsn, allow string
		ok         bool
	}{
		{"postgres://u:p@127.0.0.1:5432/ubag_bench", "", true},
		{"postgres://u:p@localhost/ubag_test?sslmode=disable", "", true},
		{"postgres://u:p@10.1.2.3/bench", "", true},
		{"host=/var/run/postgresql dbname=bench_x", "", true},
		{"postgres://u:p@db.example.com/ubag_bench", "", false},
		{"postgres://u:p@db.example.com/ubag_bench", "db.example.com", true},
		{"postgres://u:p@db.example.com/ubag_bench", "*", false},
		{"postgres://u:p@127.0.0.1/ubag", "", false},
		{"postgres://u:p@127.0.0.1,db.example.com/bench", "", false},
		{"postgres://u:p@8.8.8.8/bench", "", false},
	} {
		if err := CheckDSN(c.dsn, c.allow); (err == nil) != c.ok {
			t.Errorf("CheckDSN(%q, %q) err=%v, want ok=%v", c.dsn, c.allow, err, c.ok)
		}
	}
}
