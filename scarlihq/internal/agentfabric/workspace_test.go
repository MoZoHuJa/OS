package agentfabric

import (
	"strings"
	"testing"
	"time"
)

// validWorkspaceID is a syntactically valid workspace ID for tests.
func validWorkspaceID() string {
	return "ws-" + strings.Repeat("a", 32)
}

// validSessionID is a syntactically valid kernel session ID for tests.
func validSessionID() string {
	return "aks-" + strings.Repeat("b", 32)
}

func TestWorkspaceIDValidate(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{validWorkspaceID(), true},
		{"", false},
		{"ws-", false},
		{"ws-abc", false},
		{"ws-" + strings.Repeat("a", 31), false},
		{"ws-" + strings.Repeat("a", 33), false},
		{"ws-" + strings.Repeat("A", 32), false}, // uppercase hex rejected
		{"ws-" + strings.Repeat("g", 32), false}, // non-hex rejected
		{"wx-" + strings.Repeat("a", 32), false}, // wrong prefix
		{"aks-" + strings.Repeat("a", 32), false},
		{strings.Repeat("a", 35), false},
	}
	for _, tc := range cases {
		err := WorkspaceID(tc.id).Validate()
		if tc.want && err != nil {
			t.Errorf("Validate(%q) = %v, want nil", tc.id, err)
		}
		if !tc.want && err == nil {
			t.Errorf("Validate(%q) = nil, want error", tc.id)
		}
	}
}

func TestValidateWorkspaceName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"alpha", true},
		{"  padded  ", true}, // trimmed
		{"project-x/2026", true},
		{"", false},
		{"   ", false},
		{strings.Repeat("x", 64), true},  // at the bound
		{strings.Repeat("x", 65), false}, // over the bound
		{"line\nbreak", false},           // control char
		{"tab\tname", false},             // control char
		{"bell\x07", false},              // control char
		{"del\x7f", false},               // DEL
	}
	for _, tc := range cases {
		err := ValidateWorkspaceName(tc.name)
		if tc.want && err != nil {
			t.Errorf("ValidateWorkspaceName(%q) = %v, want nil", tc.name, err)
		}
		if !tc.want && err == nil {
			t.Errorf("ValidateWorkspaceName(%q) = nil, want error", tc.name)
		}
	}
}

func TestValidateWorkspaceDescription(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"", true},    // optional
		{"   ", true}, // trims to empty
		{"coding context for the relwork", true},
		{strings.Repeat("d", 256), true},  // at the bound
		{strings.Repeat("d", 257), false}, // over the bound
		{"multi\nline", false},            // control char
		{"del\x7f", false},
	}
	for _, tc := range cases {
		err := ValidateWorkspaceDescription(tc.desc)
		if tc.want && err != nil {
			t.Errorf("ValidateWorkspaceDescription(%q) = %v, want nil", tc.desc, err)
		}
		if !tc.want && err == nil {
			t.Errorf("ValidateWorkspaceDescription(%q) = nil, want error", tc.desc)
		}
	}
}

func TestWorkspaceRecordValidate(t *testing.T) {
	now := time.Now().UTC()
	archived := now.Add(time.Hour)

	valid := func(mutate func(*WorkspaceRecord)) WorkspaceRecord {
		r := WorkspaceRecord{
			ID:        validWorkspaceID(),
			Name:      "relwork",
			State:     WorkspaceActive,
			Sessions:  []string{validSessionID()},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if mutate != nil {
			mutate(&r)
		}
		return r
	}
	cases := []struct {
		name  string
		rec   WorkspaceRecord
		valid bool
	}{
		{"active minimal", valid(nil), true},
		{"archived", valid(func(r *WorkspaceRecord) {
			r.State = WorkspaceArchived
			r.ArchivedAt = &archived
			r.UpdatedAt = archived
		}), true},
		{"bad id", valid(func(r *WorkspaceRecord) { r.ID = "ws-nope" }), false},
		{"empty name", valid(func(r *WorkspaceRecord) { r.Name = "" }), false},
		{"unknown state", valid(func(r *WorkspaceRecord) { r.State = WorkspaceState("DRAFT") }), false},
		{"active with archived_at", valid(func(r *WorkspaceRecord) { r.ArchivedAt = &archived }), false},
		{"archived without archived_at", valid(func(r *WorkspaceRecord) { r.State = WorkspaceArchived }), false},
		{"zero created_at", valid(func(r *WorkspaceRecord) { r.CreatedAt = time.Time{} }), false},
		{"updated before created", valid(func(r *WorkspaceRecord) { r.UpdatedAt = now.Add(-time.Minute) }), false},
		{"archived before created", valid(func(r *WorkspaceRecord) {
			r.State = WorkspaceArchived
			past := now.Add(-time.Minute)
			r.ArchivedAt = &past
			r.UpdatedAt = now
		}), false},
		{"updated before archived", valid(func(r *WorkspaceRecord) {
			r.State = WorkspaceArchived
			r.ArchivedAt = &archived
			r.UpdatedAt = now // before archivedAt
		}), false},
		{"bad member shape", valid(func(r *WorkspaceRecord) { r.Sessions = []string{"not-a-ks"} }), false},
		{"duplicate member", valid(func(r *WorkspaceRecord) {
			sid := validSessionID()
			r.Sessions = []string{sid, sid}
		}), false},
		{"empty member string", valid(func(r *WorkspaceRecord) { r.Sessions = []string{""} }), false},
	}
	for _, tc := range cases {
		err := tc.rec.validate()
		if tc.valid && err != nil {
			t.Errorf("%s: validate() = %v, want nil", tc.name, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("%s: validate() = nil, want error", tc.name)
		}
	}
}

func TestWorkspaceRecordIsMember(t *testing.T) {
	sid := validSessionID()
	r := WorkspaceRecord{Sessions: []string{sid}}
	if !r.IsMember(sid) {
		t.Error("IsMember(member) = false, want true")
	}
	if r.IsMember("aks-" + strings.Repeat("c", 32)) {
		t.Error("IsMember(non-member) = true, want false")
	}
	if r.IsMember("") {
		t.Error("IsMember(\"\") = true, want false")
	}
}

func TestCopyWorkspaceRecordIsolates(t *testing.T) {
	now := time.Now().UTC()
	r := WorkspaceRecord{
		ID:         validWorkspaceID(),
		Name:       "x",
		State:      WorkspaceArchived,
		Sessions:   []string{validSessionID()},
		CreatedAt:  now,
		ArchivedAt: &now,
		UpdatedAt:  now,
	}
	cp := copyWorkspaceRecord(r)
	cp.Sessions[0] = "aks-" + strings.Repeat("d", 32)
	*cp.ArchivedAt = now.Add(time.Hour)
	if r.Sessions[0] == cp.Sessions[0] {
		t.Error("copy shares the Sessions slice with the original")
	}
	if !r.ArchivedAt.Equal(now) {
		t.Error("copy shares the ArchivedAt pointer with the original")
	}
}
