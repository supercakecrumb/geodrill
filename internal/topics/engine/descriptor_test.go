package engine

import (
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics"
)

func TestDescriptorValidate(t *testing.T) {
	valid := sampledDescriptor()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	if err := fixedDescriptor().Validate(); err != nil {
		t.Fatalf("valid fixed descriptor rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Descriptor)
		want   string
	}{
		{"empty quiz kind", func(d *Descriptor) { d.QuizKind = "" }, "QuizKind"},
		{"nil parse", func(d *Descriptor) { d.Parse = nil }, "Parse"},
		{"empty single prompt", func(d *Descriptor) { d.PromptSingle = "" }, "PromptSingle"},
		{"no distractor cap without fixed options", func(d *Descriptor) { d.Distractors = DistractorPolicy{} }, "Distractors.Max"},
		{"fixed options plus distractor policy", func(d *Descriptor) {
			d.FixedOptions = []topics.Option{{Key: "left", Label: "Left"}}
		}, "mutually exclusive"},
		{"accept without text prompt", func(d *Descriptor) { d.PromptText = "" }, "together"},
		{"text prompt without accept", func(d *Descriptor) { d.Accept = nil }, "together"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := sampledDescriptor()
			c.mutate(&d)
			err := d.Validate()
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want mention of %q", err, c.want)
			}
		})
	}
}

func TestNewPanicsOnInvalidDescriptor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("New must panic on an invalid descriptor (wiring-time programmer error)")
		}
	}()
	New(Descriptor{})
}

func TestLabelFallback(t *testing.T) {
	d := sampledDescriptor()
	if got := d.Label("apple"); got != "Apple" {
		t.Fatalf("Label(apple) = %q, want Apple", got)
	}
	if got := d.Label("xyz"); got != "XYZ" {
		t.Fatalf("Label(xyz) = %q, want XYZ (uppercase fallback)", got)
	}
}
