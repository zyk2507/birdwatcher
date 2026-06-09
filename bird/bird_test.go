package bird

import "testing"

func TestParseBirdMajorVersion(t *testing.T) {
	tests := map[string]int{
		"2.0.7":               2,
		"v2.0.9-11-g207ac485": 2,
		"3.3.0":               3,
		"v3.0.1":              3,
		"10.0.0":              10,
		"not-a-version":       0,
		"":                    0,
	}

	for version, expected := range tests {
		if actual := parseBirdMajorVersion(version); actual != expected {
			t.Fatalf("parseBirdMajorVersion(%q) = %d, expected %d", version, actual, expected)
		}
	}
}

func TestRoutesQueryAddsIPVersionFilter(t *testing.T) {
	oldBirdVersion := BirdVersion
	oldIPVersion := IPVersion
	oldDualstack := ClientConf.Dualstack
	defer func() {
		BirdVersion = oldBirdVersion
		IPVersion = oldIPVersion
		ClientConf.Dualstack = oldDualstack
	}()

	BirdVersion = 3
	IPVersion = "4"
	ClientConf.Dualstack = false

	tests := map[string]string{
		routesQuery("all protocol 'peer'"):                        "route all protocol 'peer' where net.type = NET_IP4",
		routesWhereQuery("all", "from=192.0.2.1"):                 "route all where (from=192.0.2.1) && net.type = NET_IP4",
		routesQuery("table 'master4' all where source = RTS_BGP"): "route table 'master4' all where source = RTS_BGP && net.type = NET_IP4",
	}

	for actual, expected := range tests {
		if actual != expected {
			t.Fatalf("routes query = %q, expected %q", actual, expected)
		}
	}
}

func TestRoutesQuerySkipsIPVersionFilterForBird1AndDualstack(t *testing.T) {
	oldBirdVersion := BirdVersion
	oldIPVersion := IPVersion
	oldDualstack := ClientConf.Dualstack
	defer func() {
		BirdVersion = oldBirdVersion
		IPVersion = oldIPVersion
		ClientConf.Dualstack = oldDualstack
	}()

	IPVersion = "6"
	ClientConf.Dualstack = false
	BirdVersion = 1
	if actual := routesQuery("all"); actual != "route all" {
		t.Fatalf("routesQuery for BIRD 1 = %q, expected %q", actual, "route all")
	}

	BirdVersion = 3
	ClientConf.Dualstack = true
	if actual := routesQuery("all"); actual != "route all" {
		t.Fatalf("routesQuery for dualstack = %q, expected %q", actual, "route all")
	}
}
