package httpapi_test

import (
	"context"
	"net/http/cookiejar"
	"testing"

	"wise/apps/api/internal/auth"
)

func TestMembersCannotChangeConfiguration(t *testing.T) {
	e := setup(t)
	// A push approval, opened while the admin is signed in.
	e.login()
	taskID, runID := e.newTask("Release me")
	e.saveArtifact(runID, taskID, "release_request", map[string]any{"sha": "abc", "branch": "wise/x"})
	pushAp, err := e.tasks.CreateApproval(context.Background(), runID, taskID, "release", []string{"release_request"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewService(e.db, 0)
	if _, err := authSvc.CreateUser(context.Background(), "member1", "a-member-password-123", "member"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	e.client.Jar = jar
	body := e.expect(e.do("POST", "/auth/login", map[string]string{"username": "member1", "password": "a-member-password-123"}), 200)
	e.csrf = body["csrfToken"].(string)

	// Configuration, credentials, policies, agents and templates: admins only.
	r := e.do("PUT", "/settings/models", map[string]any{"value": map[string]any{}, "version": 1})
	if r.status != 403 {
		t.Fatalf("member changed a setting: %d %v", r.status, r.body)
	}
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "gh", "kind": "github", "secret": "github_pat_x_0123456789"}), 403)
	e.expect(e.do("POST", "/workflow-templates", map[string]any{"name": "T", "definition": map[string]any{"steps": []any{}}}), 403)
	e.expect(e.do("POST", "/improvements/analyses", nil), 403)
	// Everyday work stays open: projects, tasks, reading.
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Member work"}), 201)["id"].(string)
	e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "A member task", "start": false}), 201)
	e.expect(e.do("GET", "/settings", nil), 200)
	// Approving a push uses a git account: admins only (declining stays open).
	e.expect(e.do("POST", "/approvals/"+pushAp.ID.String()+"/approve", map[string]any{"combinedHash": pushAp.CombinedHash}), 403)

	// The admin may.
	jar2, _ := cookiejar.New(nil)
	e.client.Jar = jar2
	e.login()
	e.expect(e.do("POST", "/improvements/analyses", nil), 202)
	e.expect(e.do("POST", "/approvals/"+pushAp.ID.String()+"/approve", map[string]any{"combinedHash": pushAp.CombinedHash}), 200)
}
