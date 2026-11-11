package httpapi

import (
	"context"

	"wise/apps/api/internal/httpapi/apigen"
)

func (s *Server) GetMonitor(ctx context.Context, _ apigen.GetMonitorRequestObject) (apigen.GetMonitorResponseObject, error) {
	snap, err := s.Monitor.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return reshape[apigen.GetMonitor200JSONResponse](snap), nil
}
