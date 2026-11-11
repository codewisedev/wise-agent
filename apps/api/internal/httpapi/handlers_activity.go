package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
)

var activityKinds = map[string]bool{"task": true, "approval": true, "workflow": true, "run": true, "branch": true, "push": true, "pull_request": true, "memory": true}

func (s *Server) GetProjectActivity(ctx context.Context, req apigen.GetProjectActivityRequestObject) (apigen.GetProjectActivityResponseObject, error) {
	if _, err := s.Projects.Get(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	before := time.Now().Add(time.Minute)
	if req.Params.Before != nil {
		before = *req.Params.Before
	}
	limit := 50
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	kinds := []string{}
	if req.Params.Kinds != nil {
		for _, k := range strings.Split(*req.Params.Kinds, ",") {
			if k = strings.TrimSpace(k); activityKinds[k] {
				kinds = append(kinds, k)
			}
		}
	}
	rows, err := s.DB.ProjectActivity(ctx, dbgen.ProjectActivityParams{ProjectID: req.ProjectId, Before: before, Kinds: kinds, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	type item struct {
		At     time.Time  `json:"at"`
		Kind   string     `json:"kind"`
		Title  string     `json:"title"`
		Detail string     `json:"detail"`
		TaskID *uuid.UUID `json:"taskId"`
		RefID  uuid.UUID  `json:"refId"`
		URL    string     `json:"url"`
	}
	out := struct {
		Items []item     `json:"items"`
		Next  *time.Time `json:"next"`
	}{Items: []item{}}
	for _, r := range rows {
		it := item{At: r.At, Kind: r.Kind, Title: r.Title, Detail: r.Detail, RefID: r.RefID, URL: r.Url}
		if r.TaskID != uuid.Nil {
			id := r.TaskID
			it.TaskID = &id
		}
		out.Items = append(out.Items, it)
	}
	if len(rows) == limit {
		last := rows[len(rows)-1].At
		out.Next = &last
	}
	return reshape[apigen.GetProjectActivity200JSONResponse](out), nil
}
