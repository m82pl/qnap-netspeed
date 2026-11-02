package link

import (
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s + "\n")} }
func sym(t string) *fstest.MapFile  { return &fstest.MapFile{Data: []byte(t), Mode: fs.ModeSymlink} }
func dir() *fstest.MapFile          { return &fstest.MapFile{Mode: fs.ModeDir} }

// A TS-464-like tree: two physical ports, a bond of them under a bridge,
// plus a port whose driver reports speed -1 and one that is down.
func tree() fstest.MapFS {
	return fstest.MapFS{
		"bonding_masters": file("bond0"),
		"lo/operstate":    file("unknown"), "lo/mtu": file("65536"),
		"eth0/operstate": file("up"), "eth0/carrier": file("1"), "eth0/speed": file("2500"), "eth0/duplex": file("full"),
		"eth0/mtu": file("1500"), "eth0/address": file("24:5e:be:00:00:01"), "eth0/device": dir(),
		"eth0/device/driver": sym("../../../bus/pci/drivers/igc"), "eth0/master": sym("../bond0"),
		"eth1/operstate": file("up"), "eth1/carrier": file("1"), "eth1/speed": file("1000"), "eth1/duplex": file("full"),
		"eth1/mtu": file("1500"), "eth1/device": dir(), "eth1/master": sym("../bond0"),
		"eth2/operstate": file("down"), "eth2/carrier": file("0"), "eth2/speed": file("-1"), "eth2/duplex": file("unknown"),
		"eth2/mtu": file("1500"), "eth2/device": dir(),
		"bond0/operstate": file("up"), "bond0/mtu": file("1500"), "bond0/bonding/slaves": file("eth0 eth1"),
		"bond0/bonding/mode": file("802.3ad 4"), "bond0/master": sym("../qvs0"),
		"qvs0/operstate": file("up"), "qvs0/mtu": file("1500"), "qvs0/bridge/stp_state": file("0"),
		"qvs0/brif/bond0": dir(),
	}
}

func TestRead(t *testing.T) {
	ifs, err := Read(tree())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	by := map[string]Interface{}
	for _, i := range ifs {
		names = append(names, i.Name)
		by[i.Name] = i
	}
	if !reflect.DeepEqual(names, []string{"bond0", "eth0", "eth1", "eth2", "lo", "qvs0"}) {
		t.Fatalf("%v (bonding_masters must not be listed)", names)
	}
	e0 := by["eth0"]
	if e0.Kind != KindPhysical || *e0.SpeedMbps != 2500 || e0.Duplex != "full" || e0.MTU != 1500 ||
		e0.Driver != "igc" || e0.Master != "bond0" || e0.MAC != "24:5e:be:00:00:01" || e0.Operstate != "up" {
		t.Errorf("%+v", e0)
	}
	if by["eth2"].SpeedMbps != nil {
		t.Errorf("speed -1 must be null: %v", *by["eth2"].SpeedMbps)
	}
	if b := by["bond0"]; b.Kind != KindBond || !reflect.DeepEqual(b.Members, []string{"eth0", "eth1"}) || b.BondMode != "802.3ad 4" || b.Master != "qvs0" {
		t.Errorf("%+v", b)
	}
	if b := by["qvs0"]; b.Kind != KindBridge || !reflect.DeepEqual(b.Members, []string{"bond0"}) {
		t.Errorf("%+v", b)
	}
	if by["lo"].Kind != KindVirtual {
		t.Errorf("%+v", by["lo"])
	}
}

func TestReadOneMissing(t *testing.T) {
	if _, err := ReadOne(tree(), "eth9"); err == nil {
		t.Fatal("want error")
	}
}

func TestResolve(t *testing.T) {
	phys, sum := Resolve(tree(), "qvs0")
	if len(phys) != 2 || phys[0].Name != "eth0" || phys[1].Name != "eth1" {
		t.Fatalf("%+v", phys)
	}
	if sum != nil {
		t.Error("a bridge has no sum")
	}
	phys, sum = Resolve(tree(), "bond0")
	if len(phys) != 2 || sum == nil || *sum != 3500 {
		t.Fatalf("%+v %v", phys, sum)
	}
	phys, sum = Resolve(tree(), "eth1")
	if len(phys) != 1 || phys[0].Name != "eth1" || sum != nil {
		t.Fatalf("%+v", phys)
	}
	if phys, _ := Resolve(tree(), "nope"); len(phys) != 0 {
		t.Errorf("%+v", phys)
	}
}

func TestResolveSurvivesLoop(t *testing.T) {
	m := fstest.MapFS{"a/bridge/x": file(""), "a/brif/b": dir(), "b/bridge/x": file(""), "b/brif/a": dir()}
	if phys, _ := Resolve(m, "a"); len(phys) != 0 {
		t.Errorf("%+v", phys)
	}
}

func TestResolveBondWithoutSpeeds(t *testing.T) {
	m := tree()
	m["eth0/speed"], m["eth1/speed"] = file("-1"), file("-1")
	if _, sum := Resolve(m, "bond0"); sum != nil {
		t.Errorf("%d", *sum)
	}
}

func TestParseExpect(t *testing.T) {
	if got := ParseExpect("eth0:2500  eth1:10000"); !reflect.DeepEqual(got, map[string]int{"eth0": 2500, "eth1": 10000}) {
		t.Error(got)
	}
	if got := ParseExpect(""); len(got) != 0 {
		t.Error(got)
	}
}

func TestEvaluate(t *testing.T) {
	ifs, _ := Read(tree())
	// eth1 negotiated 1000 but 2500 expected; eth2 expected but down; eth0 fine.
	w := Evaluate(ifs, map[string]int{"eth0": 2500, "eth1": 2500, "eth2": 1000})
	if want := []string{"link_degraded:eth1", "link_down:eth2"}; !reflect.DeepEqual(w, want) {
		t.Fatalf("%v", w)
	}
	for _, i := range ifs {
		if i.Name == "eth1" && (i.ExpectedMbps == nil || *i.ExpectedMbps != 2500) {
			t.Error("expected_mbps not filled")
		}
	}
}

func TestEvaluateNoExpectationReportsOnly(t *testing.T) {
	ifs, _ := Read(tree())
	if w := Evaluate(ifs, nil); len(w) != 0 {
		t.Fatalf("must not guess an expected speed: %v", w)
	}
}

func TestEvaluateNullSpeedIsNotDegraded(t *testing.T) { // plan W6
	m := tree()
	m["eth0/speed"] = file("-1")
	ifs, _ := Read(m)
	if w := Evaluate(ifs, map[string]int{"eth0": 2500}); len(w) != 0 {
		t.Fatalf("driver not reporting must not raise link_degraded: %v", w)
	}
}

func TestEvaluateDuplexAndMTU(t *testing.T) {
	m := tree()
	m["eth1/duplex"] = file("half")
	m["eth0/mtu"] = file("9000")
	ifs, _ := Read(m)
	w := Evaluate(ifs, nil)
	if want := []string{"duplex_half:eth1", "mtu_mismatch:bond0"}; !reflect.DeepEqual(w, want) {
		t.Fatalf("%v", w)
	}
}
