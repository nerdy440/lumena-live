package pgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/admin"
	"github.com/lumena/admin/pgrepo"
	"github.com/lumena/db"
)

func newTestRepos(t *testing.T) (*pgrepo.RoleRepo, *pgrepo.AuditRepo, *pgrepo.TicketRepo) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.NewRoleRepo(pool), pgrepo.NewAuditRepo(pool), pgrepo.NewTicketRepo(pool)
}

func TestRole_UnsetThenSetThenGet(t *testing.T) {
	roles, _, _ := newTestRepos(t)
	ctx := context.Background()

	_, ok, err := roles.GetRole(ctx, "acc-1")
	if err != nil || ok {
		t.Fatalf("expected no role set, got ok=%v err=%v", ok, err)
	}

	if err := roles.SetRole(ctx, "acc-1", admin.RoleTrustSafety); err != nil {
		t.Fatalf("set role: %v", err)
	}
	role, ok, err := roles.GetRole(ctx, "acc-1")
	if err != nil || !ok || role != admin.RoleTrustSafety {
		t.Fatalf("get role: %v ok=%v err=%v", role, ok, err)
	}

	// SetRole again overwrites.
	if err := roles.SetRole(ctx, "acc-1", admin.RoleSuperAdmin); err != nil {
		t.Fatalf("re-set role: %v", err)
	}
	role2, _, _ := roles.GetRole(ctx, "acc-1")
	if role2 != admin.RoleSuperAdmin {
		t.Fatalf("expected role overwritten to superadmin, got %v", role2)
	}
}

func TestAudit_AppendThenListNewestFirst(t *testing.T) {
	_, audit, _ := newTestRepos(t)
	ctx := context.Background()

	e1, err := audit.Append(ctx, admin.AuditEntry{ActorID: "admin-1", ActorRole: admin.RoleTrustSafety, Action: "restrict_account", TargetType: "account", TargetID: "acc-1"})
	if err != nil || e1.ID == "" {
		t.Fatalf("append 1: %+v err=%v", e1, err)
	}
	e2, err := audit.Append(ctx, admin.AuditEntry{ActorID: "admin-1", ActorRole: admin.RoleTrustSafety, Action: "unsuspend_account", TargetType: "account", TargetID: "acc-1"})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}

	list, err := audit.List(ctx, 10)
	if err != nil || len(list) != 2 || list[0].ID != e2.ID {
		t.Fatalf("expected newest first (e2, e1), got %+v err=%v", list, err)
	}

	limited, err := audit.List(ctx, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit=1: %+v err=%v", limited, err)
	}
}

func TestTicket_CreateWithFirstMessageThenReplyThenResolve(t *testing.T) {
	_, _, tickets := newTestRepos(t)
	ctx := context.Background()

	ticket, err := tickets.Create(ctx,
		admin.SupportTicket{AccountID: "acc-1", Subject: "payout stuck", Category: "billing", Status: admin.TicketOpen, SLADeadline: time.Now().Add(admin.FirstResponseSLA)},
		admin.TicketMessage{AuthorID: "acc-1", IsAdmin: false, Body: "my payout has been pending for a week"},
	)
	if err != nil || ticket.ID == "" {
		t.Fatalf("create: %+v err=%v", ticket, err)
	}

	msgs, err := tickets.ListMessages(ctx, ticket.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "my payout has been pending for a week" {
		t.Fatalf("first message not stored: %+v err=%v", msgs, err)
	}

	reply, err := tickets.AddMessage(ctx, ticket.ID, admin.TicketMessage{AuthorID: "admin-1", IsAdmin: true, Body: "looking into it"})
	if err != nil || reply.ID == "" {
		t.Fatalf("add message: %+v err=%v", reply, err)
	}
	msgs2, _ := tickets.ListMessages(ctx, ticket.ID)
	if len(msgs2) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs2))
	}

	openCount, err := tickets.CountOpen(ctx)
	if err != nil || openCount != 1 {
		t.Fatalf("count open = %d, want 1 (err=%v)", openCount, err)
	}

	if err := tickets.UpdateStatusAssignee(ctx, ticket.ID, admin.TicketResolved, "admin-1"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, err := tickets.Get(ctx, ticket.ID)
	if err != nil || got.Status != admin.TicketResolved || got.AssignedTo != "admin-1" {
		t.Fatalf("resolve not persisted: %+v err=%v", got, err)
	}

	openCount2, _ := tickets.CountOpen(ctx)
	if openCount2 != 0 {
		t.Fatalf("count open after resolve = %d, want 0", openCount2)
	}
}

func TestTicket_ListByAccountAndListAllFilter(t *testing.T) {
	_, _, tickets := newTestRepos(t)
	ctx := context.Background()
	t1, _ := tickets.Create(ctx, admin.SupportTicket{AccountID: "acc-1", Subject: "a", Status: admin.TicketOpen, SLADeadline: time.Now()}, admin.TicketMessage{AuthorID: "acc-1", Body: "hi"})
	_, _ = tickets.Create(ctx, admin.SupportTicket{AccountID: "acc-2", Subject: "b", Status: admin.TicketResolved, SLADeadline: time.Now()}, admin.TicketMessage{AuthorID: "acc-2", Body: "hi"})

	byAccount, err := tickets.ListByAccount(ctx, "acc-1")
	if err != nil || len(byAccount) != 1 || byAccount[0].ID != t1.ID {
		t.Fatalf("list by account: %+v err=%v", byAccount, err)
	}

	openOnly, err := tickets.ListAll(ctx, admin.TicketOpen)
	if err != nil || len(openOnly) != 1 || openOnly[0].ID != t1.ID {
		t.Fatalf("list all open: %+v err=%v", openOnly, err)
	}

	all, err := tickets.ListAll(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %+v err=%v", all, err)
	}
}
