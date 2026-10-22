package steps

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestRouteObservationProbe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires sh")
	}
	for _, tc := range []struct {
		name, ipv6 string
		want       int64
	}{
		{"loopback only", "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n", 0},
		{"IPv6 connectivity", "20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			v4, v6 := filepath.Join(dir, "route"), filepath.Join(dir, "ipv6_route")
			if err := os.WriteFile(v4, []byte("Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(v6, []byte(tc.ipv6), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("sh", "-c", routeObservationProbe(v4, v6)).CombinedOutput()
			if err != nil {
				t.Fatalf("probe: %v: %s", err, out)
			}
			got := domain.ParseContainerObservation(string(out))
			if got.Routes == nil || *got.Routes != 0 || got.RoutesIPv6 == nil || *got.RoutesIPv6 != tc.want {
				t.Fatalf("probe returned %s", out)
			}
			if err := os.Remove(v6); err != nil {
				t.Fatal(err)
			}
			out, err = exec.Command("sh", "-c", routeObservationProbe(v4, v6)).CombinedOutput()
			if err != nil {
				t.Fatalf("missing table probe: %v: %s", err, out)
			}
			if got := domain.ParseContainerObservation(string(out)); got.RoutesIPv6 != nil {
				t.Fatalf("missing table observed: %s", out)
			}
		})
	}
}
