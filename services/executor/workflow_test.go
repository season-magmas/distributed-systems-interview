package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowValidation(t *testing.T) {
	cases := []struct {
		name, contents string
		valid          bool
	}{
		{"default fallback", "workflows:\n- name: assay\n  steps:\n  - name: fill\n    device: liquid-handler-1\n", true},
		{"missing default", "default: absent\nworkflows:\n- name: assay\n  steps:\n  - name: fill\n    device: liquid-handler-1\n", false},
		{"cycle", "workflows:\n- name: assay\n  steps:\n  - name: a\n    device: incubator-1\n    depends_on: [b]\n  - name: b\n    device: incubator-1\n    depends_on: [a]\n", false},
		{"missing dependency", "workflows:\n- name: assay\n  steps:\n  - name: a\n    device: incubator-1\n    depends_on: [absent]\n", false},
		{"duplicate step", "workflows:\n- name: assay\n  steps:\n  - name: a\n    device: incubator-1\n  - name: a\n    device: incubator-1\n", false},
		{"no steps", "workflows:\n- name: assay\n  steps: []\n", false},
		{"invalid yaml", "[", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflows.yaml")
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			set, err := LoadWorkflows(path)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
			if tc.valid {
				if set.Default() != "assay" {
					t.Fatal("default fallback")
				}
				if _, err = set.Get("absent"); err == nil {
					t.Fatal("unknown workflow accepted")
				}
			}
		})
	}
}
