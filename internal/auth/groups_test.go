package auth

import (
	"reflect"
	"testing"
)

func TestAllowedNamespaces(t *testing.T) {
	tests := []struct {
		name       string
		groups     []string
		adminGroup string
		wantAll    bool
		wantNS     map[string]bool
	}{
		{
			name:       "cluster admin sees everything",
			groups:     []string{"k8s-cluster-admins"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    true,
			wantNS:     nil,
		},
		{
			name:       "cluster manager (view) sees everything",
			groups:     []string{"k8s-managers-view"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    true,
			wantNS:     nil,
		},
		{
			name:       "editor of one project",
			groups:     []string{"k8s-softaml-edit"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{"softaml": true},
		},
		{
			name:       "viewer of one project",
			groups:     []string{"k8s-softaml-view"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{"softaml": true},
		},
		{
			name:       "member of multiple projects",
			groups:     []string{"k8s-softaml-edit", "k8s-xm113-view"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{"softaml": true, "xm113": true},
		},
		{
			name:       "admin + project membership → still allowAll",
			groups:     []string{"k8s-cluster-admins", "k8s-softaml-edit"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    true,
			wantNS:     nil,
		},
		{
			name:       "hyphenated project name",
			groups:     []string{"k8s-soft-guarantee-edit"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{"soft-guarantee": true},
		},
		{
			name:       "unrelated Keycloak group ignored",
			groups:     []string{"realm-admin", "default-roles"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{},
		},
		{
			name:       "no groups at all",
			groups:     nil,
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{},
		},
		{
			name:       "empty adminGroup falls back to k8s-cluster-admins",
			groups:     []string{"k8s-cluster-admins"},
			adminGroup: "",
			wantAll:    true,
			wantNS:     nil,
		},
		{
			name:       "malformed group missing ns segment",
			groups:     []string{"k8s--edit"},
			adminGroup: "k8s-cluster-admins",
			wantAll:    false,
			wantNS:     map[string]bool{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAll, gotNS := AllowedNamespaces(tt.groups, tt.adminGroup)
			if gotAll != tt.wantAll {
				t.Errorf("allowAll = %v, want %v", gotAll, tt.wantAll)
			}
			if !reflect.DeepEqual(gotNS, tt.wantNS) {
				t.Errorf("namespaces = %v, want %v", gotNS, tt.wantNS)
			}
		})
	}
}
