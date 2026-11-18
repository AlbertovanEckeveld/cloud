//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func (e *testEnv) newGroup(t *testing.T, actor Session) Group {
	t.Helper()
	g, err := e.svc.CreateGroup(context.Background(), e.client, actor, GroupInput{
		Name: fmt.Sprintf("it-g-%d-%d", time.Now().UnixNano()%1e9, testSeq.Add(1)), Description: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := e.pool.Exec(context.Background(), `DELETE FROM identity.groups WHERE id = $1`, g.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return g
}

func TestInviteUser(t *testing.T) {
	e := newTestEnv(t)
	admin, actor := e.newAdmin(t)
	ctx := context.Background()

	name := fmt.Sprintf("it-inv-%d", testSeq.Add(1))
	u, link, err := e.svc.InviteUser(ctx, e.client, actor, NewUser{Username: name, DisplayName: "Invited"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM identity.audit_events WHERE subject_id = $1`, `DELETE FROM identity.users WHERE id = $1`} {
			if _, err := e.pool.Exec(context.Background(), q, u.ID); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	if !strings.Contains(link.Link, "/activate#") || link.Emailed {
		t.Fatalf("activation = %+v", link)
	}
	got, err := e.svc.GetUser(ctx, u.ID)
	if err != nil || got.Status != StatusPending || len(got.Roles) != 0 {
		t.Fatalf("invited user = %+v, %v", got, err)
	}
	// The audit log records which administrator did it.
	var actorID uuid.UUID
	if err := e.pool.QueryRow(ctx, `SELECT actor_id FROM identity.audit_events WHERE subject_id = $1 AND event = $2`,
		u.ID, EventUserCreated).Scan(&actorID); err != nil || actorID != admin.ID {
		t.Fatalf("audit actor = %v, %v", actorID, err)
	}
}

func TestAdminChangesNeedRecentAuth(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	target := e.newUser(t)
	ctx := context.Background()
	e.advance(recentAuthWindow + time.Minute)

	checks := map[string]error{
		"invite": func() error {
			_, _, err := e.svc.InviteUser(ctx, e.client, actor, NewUser{Username: "it-never", DisplayName: "x"})
			return err
		}(),
		"update":  e.svc.UpdateUser(ctx, e.client, e.as(t, actor), target.ID, UserUpdate{DisplayName: new("x")}),
		"disable": e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), target.ID, true),
		"reset":   func() error { _, err := e.svc.ResetUser(ctx, e.client, e.as(t, actor), target.ID); return err }(),
		"group": func() error {
			_, err := e.svc.CreateGroup(ctx, e.client, actor, GroupInput{Name: "it-never"})
			return err
		}(),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrReauthRequired) {
			t.Errorf("%s: err = %v, want ErrReauthRequired", name, err)
		}
	}
}

func TestAdminCannotLockThemselvesOut(t *testing.T) {
	e := newTestEnv(t)
	admin, actor := e.newAdmin(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"disable": e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), admin.ID, true),
		"reset":   func() error { _, err := e.svc.ResetUser(ctx, e.client, e.as(t, actor), admin.ID); return err }(),
	} {
		if !errors.Is(err, ErrSelf) {
			t.Errorf("%s self: err = %v, want ErrSelf", name, err)
		}
	}
}

func activeAdmins(t *testing.T, e *testEnv) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM identity.users u WHERE u.disabled_at IS NULL AND u.id IN (
			SELECT user_id FROM identity.role_assignments WHERE role_id = $1 AND user_id IS NOT NULL
			UNION SELECT m.user_id FROM identity.role_assignments a JOIN identity.group_members m ON m.group_id = a.group_id
			WHERE a.role_id = $1)`, AdminRoleID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentMutualDemotionKeepsAnAdmin(t *testing.T) {
	e := newTestEnv(t)
	others := activeAdmins(t, e) // real administrators in the shared dev database
	a, sessA := e.newAdmin(t)
	b, sessB := e.newAdmin(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	asA, asB := e.as(t, sessA), e.as(t, sessB)
	wg.Go(func() { errs[0] = e.svc.SetRole(ctx, e.client, asA, AdminRoleID, RoleSubject{User: &b.ID}, false) })
	wg.Go(func() { errs[1] = e.svc.SetRole(ctx, e.client, asB, AdminRoleID, RoleSubject{User: &a.ID}, false) })
	wg.Wait()

	if activeAdmins(t, e) < 1 {
		t.Fatal("no administrator left")
	}
	if others == 0 {
		failed := 0
		for _, err := range errs {
			if errors.Is(err, ErrLastAdmin) {
				failed++
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if failed != 1 {
			t.Fatalf("errors = %v, want exactly one ErrLastAdmin", errs)
		}
	}
}

func TestDisableAndEnableUser(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	u := e.newUser(t)
	secret, token := e.activate(t, u)
	ctx := context.Background()

	if err := e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Error("session survived disabling")
	}
	if _, err := e.svc.Login(ctx, e.client, u.Username, testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled login: err = %v, want generic ErrInvalidCredentials", err)
	}
	if _, err := e.svc.IssueActivation(ctx, e.client, u.Username); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("activation for disabled user: err = %v", err)
	}
	if got, _ := e.svc.GetUser(ctx, u.ID); got.Status != StatusDisabled || !got.HasTOTP {
		t.Errorf("disabled user = %+v", got)
	}

	if err := e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), u.ID, false); err != nil {
		t.Fatal(err)
	}
	e.login(t, u, testPassword, secret)
	if got, _ := e.svc.GetUser(ctx, u.ID); got.Status != StatusActive || got.LastLoginAt == nil {
		t.Errorf("after enabling: %+v", got)
	}
}

func TestUpdateUser(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	u := e.newUser(t)
	ctx := context.Background()

	if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), u.ID, UserUpdate{DisplayName: new("  Nieuwe Naam  ")}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.GetUser(ctx, u.ID)
	if got.DisplayName != "Nieuwe Naam" {
		t.Fatalf("after update: %+v", got)
	}
	if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), u.ID, UserUpdate{DisplayName: new("")}); err == nil {
		t.Error("empty display name accepted")
	}
	if err := e.svc.UpdateUser(ctx, e.client, e.as(t, actor), uuid.New(), UserUpdate{DisplayName: new("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v", err)
	}
}

func TestGroups(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	u := e.newUser(t)
	ctx := context.Background()
	g := e.newGroup(t, actor)

	if _, err := e.svc.CreateGroup(ctx, e.client, actor, GroupInput{Name: g.Name}); !errors.Is(err, ErrGroupExists) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := e.svc.CreateGroup(ctx, e.client, actor, GroupInput{Name: "Bad Name"}); err == nil {
		t.Error("invalid name accepted")
	}

	// Membership changes are idempotent.
	for range 2 {
		if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, actor), g.ID, u.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := e.svc.GetGroup(ctx, g.ID)
	if err != nil || len(detail.Members) != 1 || detail.Members[0].ID != u.ID {
		t.Fatalf("members = %+v, %v", detail.Members, err)
	}
	if refs, _ := e.svc.UserGroups(ctx, u.ID); len(refs) != 1 || refs[0].Name != g.Name {
		t.Errorf("user groups = %+v", refs)
	}
	if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, actor), g.ID, uuid.New(), true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v", err)
	}

	renamed := g.Name + "-x"
	if err := e.svc.UpdateGroup(ctx, e.client, actor, g.ID, GroupInput{Name: renamed, Description: "nieuw"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, actor), g.ID, u.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	if refs, _ := e.svc.UserGroups(ctx, u.ID); len(refs) != 0 {
		t.Errorf("still member: %+v", refs)
	}
	if err := e.svc.DeleteGroup(ctx, e.client, e.as(t, actor), g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetGroup(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted group: err = %v", err)
	}
}

func TestListAuditPagination(t *testing.T) {
	e := newTestEnv(t)
	admin, actor := e.newAdmin(t)
	ctx := context.Background()
	g := e.newGroup(t, actor)
	for range 3 {
		if err := e.svc.UpdateGroup(ctx, e.client, actor, g.ID, GroupInput{Name: g.Name}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := e.svc.ListAudit(ctx, nil, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %d, %v", len(first), err)
	}
	if first[0].Event != EventGroupUpdated || first[0].Actor == nil || *first[0].Actor != admin.Username || first[0].Details["group"] != g.Name {
		t.Fatalf("newest entry = %+v", first[0])
	}
	second, err := e.svc.ListAudit(ctx, &first[1].ID, 2)
	if err != nil || len(second) != 2 {
		t.Fatalf("second page = %d, %v", len(second), err)
	}
	if second[0].ID.String() >= first[1].ID.String() {
		t.Error("pages overlap or are out of order")
	}
}
