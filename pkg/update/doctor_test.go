package update

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/doctor"
	"github.com/DomBlack/git-stack/pkg/version"
)

func TestDoctorUpdate(t *testing.T) {
	ctx := context.Background()
	gh := newFakeGitHub(t, "v1.3.0", "new binary")
	tgt := target(t)
	var refreshed string
	d := &Doctor{Client: gh.client(), Options: Options{Current: release("v1.2.0"), Target: tgt, GOOS: "darwin", GOARCH: "arm64"},
		AfterUpdate: func(_ context.Context, exe string) error { refreshed = exe; return nil }}
	c := d.Checks(nil)[0]
	if f := c.Run(ctx); f.Status != doctor.Warn || f.Summary != "v1.3.0 is out, you have v1.2.0" {
		t.Errorf("behind = %+v", f)
	}
	if err := c.Fix(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(tgt); string(got) != "new binary" || refreshed != tgt {
		t.Errorf("binary %q, refreshed %q", got, refreshed)
	}
	if f := c.Run(ctx); f.Status != doctor.OK || !strings.Contains(f.Summary, "updated from v1.2.0 to v1.3.0") {
		t.Errorf("after the fix = %+v", f)
	}

	for v, want := range map[version.Info]string{
		release("v1.3.0"): "v1.3.0, the latest release",
		{Version: "dev"}:  "a dev build",
	} {
		d := &Doctor{Client: gh.client(), Options: Options{Current: v, Target: tgt, GOOS: "darwin", GOARCH: "arm64"}}
		c := d.Checks(nil)[0]
		if f := c.Run(ctx); f.Status != doctor.OK || !strings.Contains(f.Summary, want) {
			t.Errorf("%v = %+v", v, f)
		}
		if c.Fix != nil {
			t.Error("without AfterUpdate there is no fix")
		}
	}
}
