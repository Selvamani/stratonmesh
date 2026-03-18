package catalog_test

import (
	"testing"

	"github.com/selvamani/stratonmesh/pkg/catalog"
)

func TestDefaultProfiles_Present(t *testing.T) {
	for _, size := range []string{"XS", "S", "M", "L", "XL"} {
		p, ok := catalog.DefaultProfiles[size]
		if !ok {
			t.Errorf("missing default profile %q", size)
			continue
		}
		if p.CPU == "" {
			t.Errorf("profile %q: CPU is empty", size)
		}
		if p.Memory == "" {
			t.Errorf("profile %q: Memory is empty", size)
		}
		if p.Replicas <= 0 {
			t.Errorf("profile %q: Replicas = %d, want > 0", size, p.Replicas)
		}
	}
}

func TestEngine_GetProfile(t *testing.T) {
	eng := catalog.New(nil, nil)

	p, ok := eng.GetProfile("M")
	if !ok {
		t.Fatal("GetProfile(M) returned false")
	}
	if p.Name != "M" {
		t.Errorf("Name = %q, want M", p.Name)
	}
}

func TestEngine_GetProfile_CaseInsensitive(t *testing.T) {
	eng := catalog.New(nil, nil)
	_, ok := eng.GetProfile("m")
	if !ok {
		t.Error("GetProfile(m) should be case-insensitive")
	}
}

func TestEngine_AddCustomProfile(t *testing.T) {
	eng := catalog.New(nil, nil)
	eng.AddProfile(catalog.SizeProfile{
		Name:        "NANO",
		CPU:         "50m",
		Memory:      "64Mi",
		Replicas:    1,
		MaxReplicas: 1,
	})
	p, ok := eng.GetProfile("NANO")
	if !ok {
		t.Fatal("custom profile NANO not found")
	}
	if p.CPU != "50m" {
		t.Errorf("CPU = %q, want 50m", p.CPU)
	}
}

func TestEngine_ListProfiles(t *testing.T) {
	eng := catalog.New(nil, nil)
	profiles := eng.ListProfiles()
	if len(profiles) < 5 {
		t.Errorf("expected at least 5 profiles, got %d", len(profiles))
	}
}

func TestEngine_UnknownProfile(t *testing.T) {
	eng := catalog.New(nil, nil)
	_, ok := eng.GetProfile("UNKNOWN")
	if ok {
		t.Error("GetProfile(UNKNOWN) should return false")
	}
}
