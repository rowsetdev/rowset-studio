package api

import (
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestEffectiveNodeRoleEnforcesRolePolicy(t *testing.T) {
	primary, secondary := "primary", "secondary"
	tests := []struct {
		name, policy, fallback, want string
		requested                    *string
	}{
		{"primary policy ignores secondary request", "primary_only", "secondary", "primary", &secondary},
		{"secondary policy ignores primary request", "secondary_only", "primary", "secondary", &primary},
		{"selectable honors request", "user_selectable", "primary", "secondary", &secondary},
		{"selectable uses default", "user_selectable", "secondary", "secondary", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := effectiveNodeRole(test.policy, test.fallback, test.requested)
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := effectiveNodeRole("invalid", "primary", nil); err == nil {
		t.Fatal("invalid policy was accepted")
	}
	invalid := "replica"
	if _, err := effectiveNodeRole("user_selectable", "primary", &invalid); err == nil {
		t.Fatal("invalid requested role was accepted")
	}
}

func TestFreshNodeFailsClosedForStaleOrWritableSecondary(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stale := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	server := &Server{config: config.Config{TopologyCheckIntervalSecs: 10}}
	nodes := []domain.ConnectionNode{
		{Name: "stale", DetectedRole: "primary", Health: "healthy", LastCheckedAt: &stale},
		{Name: "writable-replica", DetectedRole: "secondary", Health: "healthy", ReadOnly: false, LastCheckedAt: &now},
		{Name: "verified-replica", DetectedRole: "secondary", Health: "healthy", ReadOnly: true, LastCheckedAt: &now},
	}
	if _, ok := server.freshNode(nodes, "primary"); ok {
		t.Fatal("stale primary was routed")
	}
	got, ok := server.freshNode(nodes, "secondary")
	if !ok || got.Name != "verified-replica" {
		t.Fatalf("unexpected secondary selection: %#v, %v", got, ok)
	}
}

func TestTopologyTruthyHandlesDriverRepresentations(t *testing.T) {
	for _, value := range []any{true, int64(1), int32(1), 1, "on", "TRUE"} {
		if !topologyTruthy(value) {
			t.Fatalf("expected truthy: %#v", value)
		}
	}
	for _, value := range []any{false, int64(0), "off", nil} {
		if topologyTruthy(value) {
			t.Fatalf("expected false: %#v", value)
		}
	}
}
