package lima

import "testing"

const ndjson = `{"name":"default","status":"Stopped","dir":"/Users/me/.lima/default","sshLocalPort":60022}
{"name":"cracklet","status":"Running","dir":"/Users/me/.lima/cracklet","sshLocalPort":60023}
`

func TestParseListNDJSON(t *testing.T) {
	instances, err := ParseList([]byte(ndjson))
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(instances))
	}
	if instances[1].Name != "cracklet" || instances[1].Status != StatusRunning || instances[1].Dir != "/Users/me/.lima/cracklet" {
		t.Errorf("unexpected instance: %+v", instances[1])
	}
}

func TestParseListArray(t *testing.T) {
	instances, err := ParseList([]byte(`[{"name":"cracklet","status":"Stopped"}]`))
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	if len(instances) != 1 || instances[0].Status != StatusStopped {
		t.Errorf("unexpected instances: %+v", instances)
	}
}

func TestParseListEmpty(t *testing.T) {
	instances, err := ParseList([]byte("\n"))
	if err != nil || len(instances) != 0 {
		t.Errorf("expected no instances and no error, got %v, %v", instances, err)
	}
}

func TestParseListGarbage(t *testing.T) {
	if _, err := ParseList([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestFindInstance(t *testing.T) {
	instances, _ := ParseList([]byte(ndjson))
	if inst, ok := Find(instances, "cracklet"); !ok || inst.Name != "cracklet" {
		t.Errorf("Find(cracklet) = %+v, %v", inst, ok)
	}
	if _, ok := Find(instances, "nope"); ok {
		t.Error("Find(nope) should not match")
	}
}

func TestParseListReadsSize(t *testing.T) {
	instances, err := ParseList([]byte(`{"name":"cracklet","status":"Running","cpus":4,"memory":8589934592,"disk":42949672960}`))
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	got := instances[0]
	if got.CPUs != 4 || got.Memory != 8<<30 || got.Disk != 40<<30 {
		t.Errorf("size not decoded: %+v", got)
	}
}
