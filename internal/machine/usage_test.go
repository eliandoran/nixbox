package machine

import "testing"

func TestParseUsages(t *testing.T) {
	const out = `Id=container@web.service
ActiveState=active
MemoryCurrent=104857600
CPUUsageNSec=5000000000
TasksCurrent=12

Id=container@db.service
ActiveState=inactive
MemoryCurrent=[not set]
CPUUsageNSec=18446744073709551615
TasksCurrent=[not set]
`
	unitToName := map[string]string{
		"container@web.service": "web",
		"container@db.service":  "db",
	}
	got := parseUsages(out, unitToName)
	web, ok := got["web"]
	if !ok {
		t.Fatal("missing web")
	}
	if !web.Running || web.MemBytes != 104857600 || web.CPUNSec != 5000000000 || web.Tasks != 12 {
		t.Fatalf("web = %+v", web)
	}
	db, ok := got["db"]
	if !ok {
		t.Fatal("missing db")
	}
	// Sentinels ([not set], UINT64_MAX) collapse to zero; inactive → not running.
	if db.Running || db.MemBytes != 0 || db.CPUNSec != 0 || db.Tasks != 0 {
		t.Fatalf("db = %+v", db)
	}
}
