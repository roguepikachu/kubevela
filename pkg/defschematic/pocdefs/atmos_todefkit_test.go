package pocdefs

import (
	"testing"
)

func TestAtmosS3ToDefkit(t *testing.T) {
	d, err := AtmosS3V1().ToDefkit()
	if err != nil {
		t.Fatalf("ToDefkit: %v", err)
	}
	if d.Health == nil || d.Health.Type != "crossplaneClaim" {
		t.Fatalf("expected crossplane health, got %#v", d.Health)
	}
	if d.Status == nil {
		t.Fatal("expected status")
	}
	if d.Template == nil || d.Template.Output == nil {
		t.Fatal("expected template output")
	}
	var hasSpread, hasCondStruct bool
	for _, op := range d.Template.Output.Fields {
		if op.Spread {
			hasSpread = true
		}
		if len(op.StructFields) > 0 {
			hasCondStruct = true
		}
	}
	if !hasSpread {
		t.Fatal("expected SpreadIf tags op")
	}
	if !hasCondStruct {
		t.Fatal("expected ConditionalStruct replication op")
	}
	if len(d.Validators) == 0 {
		t.Fatal("expected validators")
	}
}

func TestAtmosEfsToDefkit(t *testing.T) {
	d, err := AtmosEfsV1().ToDefkit()
	if err != nil {
		t.Fatalf("ToDefkit: %v", err)
	}
	if len(d.Helpers) == 0 || d.Helpers[0].Kind != "claimName" {
		t.Fatalf("expected ClaimName helper, got %#v", d.Helpers)
	}
	if d.Status == nil {
		t.Fatal("expected status")
	}
}

func TestAtmosEfsVolumeToDefkit(t *testing.T) {
	d, err := AtmosEfsVolumeV1().ToDefkit()
	if err != nil {
		t.Fatalf("ToDefkit: %v", err)
	}
	if d.Template == nil || d.Template.Output == nil {
		t.Fatal("expected template output")
	}
}

func TestAtmosS3ToCueStillWorks(t *testing.T) {
	out := AtmosS3V1().ToCue()
	if out == "" {
		t.Fatal("empty cue")
	}
}
